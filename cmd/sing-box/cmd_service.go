package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const defaultManagedServiceName = "sing-box-managed"
const managedServiceCommand = "managed-service"

var (
	managedServiceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)
	serviceNameFlag           string
	serviceURLFlag            string
	serviceIntervalFlag       time.Duration
	commandService            = &cobra.Command{Use: "service", Short: "Manage the system service"}
	commandServiceInstall     = &cobra.Command{
		Use:   "install",
		Short: "Install or update and start the system service",
		Args:  cobra.NoArgs,
		RunE:  runServiceInstall,
	}
	commandServiceUninstall = &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the system service, keeping configuration and data",
		Args:  cobra.NoArgs,
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
		Use:   "status",
		Short: "Show system service status and paths",
		Args:  cobra.NoArgs,
		RunE:  runServiceStatus,
	}
	serviceRunner        managedService
	commandServiceRunner = &cobra.Command{
		Use:    managedServiceCommand,
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireServiceAdmin(); err != nil {
				return err
			}
			var err error
			serviceRunner.executable, err = managedExecutable()
			if err != nil {
				return err
			}
			if err := serviceRunner.validate(); err != nil {
				return err
			}
			return runManagedService(cmd.Context(), serviceRunner)
		},
	}
)

func init() {
	mainCommand.AddCommand(commandService, commandServiceRunner)
	commandService.AddCommand(commandServiceInstall, commandServiceUninstall, commandServiceStatus)
	serviceRunner.bindFlags(commandServiceRunner.Flags())
	commandService.PersistentPreRun = prepareServiceCommand
	commandServiceRunner.PersistentPreRun = prepareServiceCommand
	commandService.PersistentFlags().StringVar(&serviceNameFlag, "name", defaultManagedServiceName, "System service name")
	commandServiceInstall.Flags().StringVar(&serviceURLFlag, "config-url", "", "Remote configuration URL (defaults to the existing private config-url file)")
	commandServiceInstall.Flags().DurationVar(&serviceIntervalFlag, "interval", defaultConfigSyncInterval, "Remote configuration sync interval")
}

func prepareServiceCommand(cmd *cobra.Command, args []string) {
	// Do not change working directory or create a directory through the ordinary run/check hook.
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

func (s managedService) configPath() string {
	return filepath.Join(s.configDir, serviceConfigFilename)
}

func (s managedService) urlPath() string {
	return filepath.Join(s.configDir, configURLFilename)
}

func (s managedService) controlDir() string {
	return filepath.Join(s.dataDir, "."+s.name+"-service")
}

func (s managedService) args() []string {
	return []string{managedServiceCommand, "--name=" + s.name, "--config-dir=" + s.configDir,
		"--data-dir=" + s.dataDir, "--interval=" + s.interval.String()}
}

func (s *managedService) bindFlags(flags *pflag.FlagSet) {
	flags.StringVar(&s.name, "name", "", "Registered service name")
	flags.StringVar(&s.configDir, "config-dir", "", "Absolute configuration directory")
	flags.StringVar(&s.dataDir, "data-dir", "", "Absolute data directory")
	flags.DurationVar(&s.interval, "interval", 0, "Configuration sync interval")
}

func (s managedService) validate() error {
	if err := validateServiceName(s.name); err != nil {
		return err
	}
	if !filepath.IsAbs(s.executable) || !filepath.IsAbs(s.configDir) || !filepath.IsAbs(s.dataDir) {
		return errors.New("service executable, configuration and data paths must be absolute")
	}
	if s.interval <= 0 {
		return errors.New("configuration sync interval must be positive")
	}
	return nil
}

func managedServiceFromArgs(name string, argv []string, uninstall bool) (*managedService, error) {
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return nil, errors.New("not a managed sing-box service")
	}
	s := managedService{executable: argv[0]}
	flags := pflag.NewFlagSet(managedServiceCommand, pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	s.bindFlags(flags)
	if flags.Parse(argv[1:]) == nil && flags.NArg() == 1 && flags.Arg(0) == managedServiceCommand &&
		s.name == name && s.validate() == nil {
		return &s, nil
	}
	if uninstall {
		// Older definitions are recognized only for explicit removal.
		legacy := pflag.NewFlagSet("run", pflag.ContinueOnError)
		legacy.SetOutput(io.Discard)
		var config string
		legacy.Bool("disable-color", false, "")
		sync := legacy.Bool("sync-config", false, "")
		legacy.StringVarP(&config, "config", "c", "", "")
		legacy.StringVarP(&s.dataDir, "directory", "D", "", "")
		legacy.StringVar(&s.name, "service-name", "", "")
		legacy.DurationVar(&s.interval, "sync-interval", 0, "")
		if legacy.Parse(argv[1:]) == nil && legacy.NArg() == 1 && legacy.Arg(0) == "run" && *sync &&
			s.name == name && filepath.IsAbs(config) && filepath.Base(config) == serviceConfigFilename {
			s.executable, s.configDir = argv[0], filepath.Dir(config)
			if s.validate() == nil {
				return &s, nil
			}
		}
	}
	return nil, errors.New("not a managed sing-box service; remove an older definition with service uninstall before reinstalling")
}

func (s managedService) checkPaths(create bool) error {
	if err := checkServiceExecutable(s.executable); err != nil {
		return fmt.Errorf("service executable %s: %w (install the executable at a stable administrator-owned path; use -C/-D to retain existing safe directories)", s.executable, err)
	}
	for _, dir := range []string{s.configDir, s.dataDir, s.controlDir()} {
		if err := checkServiceDirectory(dir, create); err != nil {
			return fmt.Errorf("service directory %s: %w", dir, err)
		}
	}
	if err := checkServiceControlDirectory(s.controlDir()); err != nil {
		return fmt.Errorf("service control directory %s: %w", s.controlDir(), err)
	}
	return nil
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
	existing, _, err := lookupManagedService(serviceNameFlag)
	if err != nil {
		return err
	}
	interval := serviceIntervalFlag
	if existing != nil {
		configDir, dataDir = existing.configDir, existing.dataDir
		if !cmd.Flags().Changed("interval") {
			interval = existing.interval
		}
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
	definition := managedService{name: serviceNameFlag, executable: executable, configDir: configDir, dataDir: dataDir, interval: interval}
	if err = definition.checkPaths(true); err != nil {
		return err
	}
	if cmd.Flags().Changed("config-url") {
		if _, err = parseConfigURL(serviceURLFlag); err != nil {
			return err
		}
		if err = writeManagedFile(definition.urlPath(), []byte(strings.TrimSpace(serviceURLFlag)+"\n"), true); err != nil {
			return fmt.Errorf("store configuration URL: %w", err)
		}
	} else if _, err = readPrivateURLFile(definition.urlPath()); err != nil {
		return fmt.Errorf("read configuration URL file (set --config-url to supply it): %w", err)
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
	return os.Executable()
}

func runServiceTool(ctx context.Context, executable string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, executable, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", filepath.Base(executable), strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
