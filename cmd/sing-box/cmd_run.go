package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	runtimeDebug "runtime/debug"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badjson"
	"github.com/sagernet/sing/service"

	"github.com/spf13/cobra"
)

var (
	runSyncConfig   bool
	runSyncInterval time.Duration
	runServiceName  string
)

var commandRun = &cobra.Command{
	Use:   "run",
	Short: "Run service",
	Run: func(cmd *cobra.Command, args []string) {
		err := runPlatform()
		if err != nil {
			log.Fatal(err)
		}
	},
}

func init() {
	mainCommand.AddCommand(commandRun)
	commandRun.Flags().BoolVar(&runSyncConfig, "sync-config", false, "Sync the configuration from the adjacent private config-url file")
	commandRun.Flags().DurationVar(&runSyncInterval, "sync-interval", defaultConfigSyncInterval, "Remote configuration sync interval")
	commandRun.Flags().StringVar(&runServiceName, "service-name", defaultManagedServiceName, "Managed service name")
}

type OptionsEntry struct {
	content []byte
	path    string
	options option.Options
}

func readConfigAt(path string) (*OptionsEntry, error) {
	var (
		configContent []byte
		err           error
	)
	if path == "stdin" {
		configContent, err = io.ReadAll(os.Stdin)
	} else {
		configContent, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, E.Cause(err, "read config at ", path)
	}
	return decodeConfigAt(globalCtx, configContent, path)
}

func decodeConfigAt(ctx context.Context, configContent []byte, path string) (*OptionsEntry, error) {
	options, err := json.UnmarshalExtendedContext[option.Options](ctx, configContent)
	if err != nil {
		return nil, E.Cause(err, "decode config at ", path)
	}
	return &OptionsEntry{
		content: configContent,
		path:    path,
		options: options,
	}, nil
}

func readConfig() ([]*OptionsEntry, error) {
	var optionsList []*OptionsEntry
	for _, path := range configPaths {
		optionsEntry, err := readConfigAt(path)
		if err != nil {
			return nil, err
		}
		optionsList = append(optionsList, optionsEntry)
	}
	for _, directory := range configDirectories {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return nil, E.Cause(err, "read config directory at ", directory)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") || entry.IsDir() {
				continue
			}
			optionsEntry, err := readConfigAt(filepath.Join(directory, entry.Name()))
			if err != nil {
				return nil, err
			}
			optionsList = append(optionsList, optionsEntry)
		}
	}
	sort.Slice(optionsList, func(i, j int) bool {
		return optionsList[i].path < optionsList[j].path
	})
	return optionsList, nil
}

func readConfigAndMerge() (option.Options, error) {
	optionsList, err := readConfig()
	if err != nil {
		return option.Options{}, err
	}
	return mergeOptionsList(optionsList)
}

func mergeOptionsList(optionsList []*OptionsEntry) (option.Options, error) {
	if len(optionsList) == 1 {
		return optionsList[0].options, nil
	}
	var (
		mergedMessage json.RawMessage
		err           error
	)
	for _, options := range optionsList {
		mergedMessage, err = badjson.MergeJSON(globalCtx, options.options.RawMessage, mergedMessage, false)
		if err != nil {
			return option.Options{}, E.Cause(err, "merge config at ", options.path)
		}
	}
	var mergedOptions option.Options
	err = mergedOptions.UnmarshalJSONContext(globalCtx, mergedMessage)
	if err != nil {
		return option.Options{}, E.Cause(err, "unmarshal merged config")
	}
	return mergedOptions, nil
}

func create(runContext context.Context, options option.Options) (*box.Box, context.CancelFunc, io.Closer, error) {
	if disableColor {
		if options.Log == nil {
			options.Log = &option.LogOptions{}
		}
		options.Log.DisableColor = true
	}
	ctx, cancel := context.WithCancel(service.ExtendContext(globalCtx))
	instance, err := box.New(box.Options{
		Context:                    ctx,
		Options:                    options,
		NetworkNamespaceHolderArgs: []string{"/proc/self/exe", commandNetnsHolder.Use},
	})
	if err != nil {
		cancel()
		return nil, nil, nil, E.Cause(err, "create service")
	}

	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer func() {
		signal.Stop(osSignals)
		close(osSignals)
	}()
	startCtx, finishStart := context.WithCancel(context.Background())
	go func() {
		select {
		case _, loaded := <-osSignals:
			if !loaded {
				return
			}
		case <-runContext.Done():
		}
		cancel()
		closeMonitor(startCtx)
	}()
	err = instance.Start()
	finishStart()
	if err != nil {
		cancel()
		return nil, nil, nil, E.Cause(err, "start service")
	}
	dnsController, err := startPlatformDNS(options)
	if err != nil {
		cancel()
		_ = instance.Close()
		return nil, nil, nil, E.Cause(err, "configure Darwin TUN DNS")
	}
	return instance, cancel, dnsController, nil
}

func run(ctx context.Context, pulls <-chan struct{}, started func()) error {
	syncer, err := prepareRunConfigSync(ctx)
	if err != nil {
		return err
	}
	optionsList, err := readConfig()
	if err != nil {
		return syncer.redact(err)
	}
	options, err := mergeOptionsList(optionsList)
	if err != nil {
		return syncer.redact(err)
	}
	err = runInUserNamespaceIfNeeded(options, optionsList)
	if err != nil {
		return syncer.redact(err)
	}
	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(osSignals)
	reload := pulls
	if syncer != nil {
		stopWatch := syncer.startWatch(ctx, pulls, optionsList[0].content)
		defer stopWatch()
		reload = syncer.reload
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		instance, cancel, dnsController, createErr := create(ctx, options)
		if createErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return syncer.redact(createErr)
		}
		if syncer != nil {
			syncer.publish()
		}
		if started != nil {
			started()
			started = nil
		}
		runtimeDebug.FreeOSMemory()
		for {
			var reloading bool
			select {
			case <-ctx.Done():
			case osSignal := <-osSignals:
				reloading = osSignal == syscall.SIGHUP
			case <-reload:
				reloading = true
			}
			var next option.Options
			if reloading {
				if syncer != nil {
					entry, checkErr := syncer.next()
					if checkErr != nil {
						log.Error("reload service: ", syncer.redact(checkErr))
					}
					if entry == nil {
						continue
					}
					next = entry.options
				} else if checkErr := check(); checkErr != nil {
					log.Error(E.Cause(checkErr, "reload service"))
					continue
				}
			}
			if dnsController != nil {
				if closeErr := dnsController.Close(); closeErr != nil {
					log.Error(E.Cause(syncer.redact(closeErr), "restore Darwin DNS"))
				}
			}
			cancel()
			closeCtx, closed := context.WithCancel(context.Background())
			go closeMonitor(closeCtx)
			err = instance.Close()
			closed()
			if !reloading {
				if err != nil {
					log.Error(E.Cause(syncer.redact(err), "sing-box did not close properly"))
				}
				return nil
			}
			if syncer != nil {
				options = next
			}
			break
		}
		if syncer == nil {
			options, err = readConfigAndMerge()
			if err != nil {
				return err
			}
		}
	}
}

func closeMonitor(ctx context.Context) {
	time.Sleep(C.FatalStopTimeout)
	select {
	case <-ctx.Done():
		return
	default:
	}
	log.Fatal("sing-box did not close!")
}
