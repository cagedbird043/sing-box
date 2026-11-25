package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	privateURLACL   = "D:P(A;;FA;;;SY)(A;;FA;;;BA)"
	privateURLFlags = windows.OWNER_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION
)

func protectURLFile(file *os.File) error {
	descriptor, err := windows.SecurityDescriptorFromString("O:BA" + privateURLACL)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(file.Name(), windows.SE_FILE_OBJECT, privateURLFlags|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, dacl, nil)
}

func readPrivateURLFile(path string) ([]byte, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pathPointer, windows.GENERIC_READ|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &info); err != nil {
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return nil, errors.New("configuration URL file must be a regular file")
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, privateURLFlags)
	if err != nil {
		return nil, err
	}
	if !restrictedURLDescriptor(descriptor) {
		return nil, errors.New("configuration URL file ACL must be restricted to SYSTEM and Administrators")
	}
	return io.ReadAll(file)
}

func restrictedURLDescriptor(descriptor *windows.SECURITY_DESCRIPTOR) bool {
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.IsWellKnown(windows.WinLocalSystemSid) && !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return false
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return false
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 2 {
		return false
	}
	var system, administrators bool
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(dacl, i, &ace); err != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 {
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		system = system || sid.IsWellKnown(windows.WinLocalSystemSid)
		administrators = administrators || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid)
	}
	return system && administrators
}

func checkServiceDirectory(path string, create bool) error {
	return checkWindowsServicePath(path, true, create, true)
}

func checkServiceControlDirectory(path string) error {
	return checkWindowsServicePath(path, true, false, true)
}

func checkServiceExecutable(path string) error {
	return checkServiceFile(path)
}

func checkServiceFile(path string) error {
	return checkWindowsServicePath(path, false, false, true)
}

func checkWindowsServicePath(path string, directory, create, strict bool) error {
	path = filepath.Clean(path)
	if volume := filepath.VolumeName(path); len(volume) != 2 || volume[1] != ':' {
		return errors.New("service paths must be on a local drive")
	}
	parent := filepath.Dir(path)
	if parent != path {
		if err := checkWindowsServicePath(parent, true, create, false); err != nil {
			return err
		}
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(pathPointer)
	if err != nil && create && errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		sd, sdErr := windows.SecurityDescriptorFromString("O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
		if sdErr != nil {
			return sdErr
		}
		sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
		err = windows.CreateDirectory(pathPointer, &sa)
		if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return err
		}
		attributes, err = windows.GetFileAttributes(pathPointer)
	}
	if err != nil {
		return &os.PathError{Op: "stat", Path: path, Err: err}
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || directory != (attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) {
		return fmt.Errorf("unexpected file type or reparse point at %s", path)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, privateURLFlags)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || !trustedServiceSID(owner) {
		return fmt.Errorf("%s must be owned by Administrators, SYSTEM or TrustedInstaller", path)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("cannot verify access rules for %s", path)
	}
	const deleteChild = 0x0040
	// A standard parent such as ProgramData may allow creating siblings.
	// It must not allow removing or taking ownership of the managed directory.
	writeMask := uint32(windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_ALL | deleteChild)
	if strict {
		writeMask |= windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_ATTRIBUTES |
			windows.FILE_WRITE_EA | windows.GENERIC_WRITE
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if uint32(ace.Mask)&writeMask != 0 && !trustedServiceSID(sid) {
				return fmt.Errorf("%s permits unprivileged writes", path)
			}
		default:
			return fmt.Errorf("cannot verify an access rule for %s", path)
		}
	}
	return nil
}

func trustedServiceSID(sid *windows.SID) bool {
	const trustedInstallerSID = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
	return sid != nil && (sid.IsWellKnown(windows.WinLocalSystemSid) ||
		sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) || sid.String() == trustedInstallerSID)
}
