package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sagernet/sing-box/log"

	"github.com/spf13/cobra"
)

const defaultManagedServiceName = "sing-box"

var (
	managedServiceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)
	serviceNameFlag           string
	serviceURLFlag            string
	serviceIntervalFlag       time.Duration
	commandService            = &cobra.Command{Use: "service", Short: "Manage the system service"}
	commandServiceInstall     = &cobra.Command{
		Use:              "install",
		Short:            "Install or update and start the system service",
		PersistentPreRun: prepareServiceCommand,
		Args:             cobra.NoArgs,
		RunE:             runServiceInstall,
	}
	commandServiceUninstall = &cobra.Command{
		Use:              "uninstall",
		Short:            "Stop and remove the system service, keeping configuration and data",
		PersistentPreRun: prepareServiceCommand,
		Args:             cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireServiceAdmin(); err != nil {
				return err
			}
			if err := validateServiceName(serviceNameFlag); err != nil {
				return err
			}
			return uninstallManagedService(serviceNameFlag)
		},
	}
	commandServiceStatus = &cobra.Command{
		Use:              "status",
		Short:            "Show system service status and paths",
		PersistentPreRun: prepareServiceCommand,
		Args:             cobra.NoArgs,
		RunE:             runServiceStatus,
	}
)

func init() {
	mainCommand.AddCommand(commandService)
	commandService.AddCommand(commandServiceInstall, commandServiceUninstall, commandServiceStatus)
	commandService.PersistentFlags().StringVar(&serviceNameFlag, "name", defaultManagedServiceName, "System service name")
	commandServiceInstall.Flags().StringVar(&serviceURLFlag, "config-url", "", "Remote configuration URL (defaults to the existing private config-url file)")
	commandServiceInstall.Flags().DurationVar(&serviceIntervalFlag, "interval", defaultConfigSyncInterval, "Remote configuration sync interval")
}

func prepareServiceCommand(cmd *cobra.Command, args []string) {
	// Resolve relative -C and -D against the caller's directory, then change
	// directories when validating a managed configuration, not during parsing.
	directory := workingDir
	workingDir = ""
	preRun(cmd, args)
	workingDir = directory
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
}

type managedService struct {
	name       string
	executable string
	configDir  string
	dataDir    string
	interval   time.Duration
}

func (s managedService) syncer() configSync {
	return configSync{target: filepath.Join(s.configDir, serviceConfigFilename), interval: s.interval}
}

func (s managedService) args() []string {
	return []string{"--disable-color", "-D", s.dataDir, "-c", s.syncer().target, "run", "--sync-config", "--sync-interval", s.interval.String(), "--service-name", s.name}
}

func managedServiceFromArgs(name string, argv []string) (managedService, error) {
	if len(argv) != 12 || argv[1] != "--disable-color" || argv[2] != "-D" || argv[4] != "-c" ||
		argv[6] != "run" || argv[7] != "--sync-config" || argv[8] != "--sync-interval" || argv[10] != "--service-name" || argv[11] != name ||
		!filepath.IsAbs(argv[0]) || !filepath.IsAbs(argv[3]) || !filepath.IsAbs(argv[5]) || filepath.Base(argv[5]) != serviceConfigFilename {
		return managedService{}, errors.New("invalid managed service command line")
	}
	interval, err := time.ParseDuration(argv[9])
	if err != nil || interval <= 0 {
		return managedService{}, errors.New("invalid managed service sync interval")
	}
	return managedService{name: name, executable: argv[0], configDir: filepath.Dir(argv[5]), dataDir: argv[3], interval: interval}, nil
}

func runServiceInstall(cmd *cobra.Command, args []string) error {
	if err := requireServiceAdmin(); err != nil {
		return err
	}
	if err := validateServiceName(serviceNameFlag); err != nil {
		return err
	}
	if serviceIntervalFlag <= 0 {
		return errors.New("configuration sync interval must be positive")
	}
	if len(configDirectories) > 1 {
		return errors.New("service install accepts only one configuration directory (-C)")
	}
	executable, configDir, dataDir, err := managedServiceDefaults()
	if err != nil {
		return err
	}
	if len(configDirectories) == 1 {
		configDir, err = filepath.Abs(configDirectories[0])
		if err != nil {
			return err
		}
	}
	if workingDir != "" {
		dataDir, err = filepath.Abs(workingDir)
		if err != nil {
			return err
		}
	}
	definition := managedService{name: serviceNameFlag, executable: executable, configDir: configDir, dataDir: dataDir, interval: serviceIntervalFlag}
	if err = preflightManagedService(definition); err != nil {
		return err
	}
	for _, dir := range []string{definition.configDir, definition.dataDir} {
		if err = os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	if err = os.Chdir(definition.dataDir); err != nil {
		return err
	}
	syncer := definition.syncer()
	if cmd.Flags().Changed("config-url") {
		if _, err = parseConfigURL(serviceURLFlag); err != nil {
			return err
		}
		if err = writePrivateURLFile(syncer.urlFile(), strings.TrimSpace(serviceURLFlag)); err != nil {
			return fmt.Errorf("store configuration URL: %w", err)
		}
	} else if _, err = readPrivateURLFile(syncer.urlFile()); err != nil {
		return fmt.Errorf("read configuration URL file (set --config-url to supply it): %w", err)
	}
	if _, _, err = syncer.pull(cmd.Context()); err != nil {
		if _, statErr := os.Stat(syncer.target); statErr != nil {
			return err
		}
		log.Warn("initial configuration sync: ", err)
	}
	return installManagedService(definition)
}

func runServiceStatus(cmd *cobra.Command, args []string) error {
	if err := validateServiceName(serviceNameFlag); err != nil {
		return err
	}
	definition, running, err := lookupManagedService(serviceNameFlag)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Installed: %t\nRunning: %t\n", definition != nil, running)
	if definition != nil {
		fmt.Fprintf(out, "Executable: %s\nConfiguration directory: %s\nData directory: %s\nSync interval: %s\nLog: %s\n",
			definition.executable, definition.configDir, definition.dataDir, definition.interval, managedServiceLog(*definition))
	}
	return nil
}

func validateServiceName(name string) error {
	if !managedServiceNamePattern.MatchString(name) {
		return errors.New("invalid service name: use 1 to 63 letters, digits, hyphens or underscores, starting with a letter or digit")
	}
	return nil
}

func managedExecutable() (string, error) {
	path, err := exec.LookPath(os.Args[0])
	if err != nil && !errors.Is(err, exec.ErrDot) {
		return os.Executable()
	}
	return filepath.Abs(path)
}

func configReadyPath(dir string, name string) string {
	return filepath.Join(dir, "."+name+"-config-revision")
}

func writeConfigRevision(path string, revision [32]byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".config-revision-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(revision[:])
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

func runServiceTool(ctx context.Context, executable string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, executable, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", filepath.Base(executable), strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
