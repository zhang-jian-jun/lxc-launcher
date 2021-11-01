package cmd

import (
	"errors"
	"fmt"
	"github.com/opensourceways/lxc-launcher/log"
	"github.com/opensourceways/lxc-launcher/lxd"
	"github.com/opensourceways/lxc-launcher/network"
	"github.com/opensourceways/lxc-launcher/task"
	"github.com/opensourceways/lxc-launcher/util"
	"github.com/spf13/cobra"
	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"net/http"
	"time"
)

var (
	instName          string
	lxcImage          string
	cpuResource       string
	memoryResource    string
	storagePool       string
	rootSize          string
	ingressLimit      string
	egressLimit       string
	proxyPort         int32
	networkDeviceName string
	instEnvs          []string
	startCommand      string
	mountFiles        []string
	exposePort        int32
	removeExisting    bool
	instType          string
	instProfiles      []string
	lxdSocket         string
	lxdServerAddress  string
	clientKeyPath     string
	clientCertPath    string
	lxdClient         *lxd.Client
	additionalConfig  []string
	networkProxy      *network.Proxy
	prober            *task.Prober
	statusPort        int32
	statusServer      *http.Server
)

const (
	InstanceContainer  = "container"
	InstanceVM         = "virtual-machine"
	NetworkMaxWaitTime = 120
)

func init() {
	launchCommand.PersistentFlags().StringVar(&lxdSocket, "lxd-socket", "", "lxd socket file for communicating")
	launchCommand.PersistentFlags().StringVar(&lxdServerAddress, "lxd-server-address", "", "lxd server address for communication, only work when lxd socket not specified")
	launchCommand.PersistentFlags().StringVar(&clientKeyPath, "client-key-path", "", "key path for lxd client authentication, only work when lxd socket not specified")
	launchCommand.PersistentFlags().StringVar(&clientCertPath, "client-cert-path", "", "cert path for lxd client authentication, only work when lxd socket not specified")
	launchCommand.PersistentFlags().StringVar(&instType, "instance-type", "container", "instance type container or virtual machine, default is container")
	launchCommand.PersistentFlags().StringArrayVar(&instProfiles, "instance-profiles", []string{}, "profiles will be applied to instances")
	launchCommand.PersistentFlags().StringVar(&cpuResource, "cpu-resource", "", "CPU limitation of lxc instance")
	launchCommand.PersistentFlags().StringVar(&memoryResource, "memory-resource", "", "Memory limitation of lxc instance")
	launchCommand.PersistentFlags().StringVar(&storagePool, "storage-pool", "", "Storage pool for lxc instance")
	launchCommand.PersistentFlags().StringVar(&rootSize, "root-size", "", "Root size for lxc instance")
	launchCommand.PersistentFlags().StringVar(&ingressLimit, "network-ingress", "", "Ingress limit for lxc instance")
	launchCommand.PersistentFlags().StringVar(&egressLimit, "network-egress", "", "Egress limit for lxc instance")
	launchCommand.PersistentFlags().Int32Var(&proxyPort, "proxy-port", 0, "Proxy port, used to forward requests to lxc instance, for example: tcp:<ip-address>:80, empty means no forwarding")
	launchCommand.PersistentFlags().StringVar(&networkDeviceName, "device-name", "eth0", "default network device name, can be used for request forwarding ")
	launchCommand.PersistentFlags().StringArrayVar(&instEnvs, "instance-envs", []string{}, "Instance environment, for example: ENV=production.")
	launchCommand.PersistentFlags().StringVar(&startCommand, "start-command", "", "Instance startup command (non-interactive & short-term), for example: systemctl start nginx.")
	launchCommand.PersistentFlags().StringArrayVar(&mountFiles, "mount-files", []string{}, "Mount files into instance in the format of <source>:<destination>")
	launchCommand.PersistentFlags().Int32Var(&exposePort, "expose-port", 8080, "Expose port for lxc proxy address")
	launchCommand.PersistentFlags().StringArrayVar(&additionalConfig, "additional-config", []string{}, "Additional config for lxd instance, in the format of `--additional-config key=value`")
	launchCommand.PersistentFlags().BoolVar(&removeExisting, "remove-existing", true, "Whether to remove existing lxc instance")
	launchCommand.PersistentFlags().Int32Var(&statusPort, "status-port", 8082, "health server port")
	launchCommand.MarkPersistentFlagRequired("storage-pool")
	rootCmd.AddCommand(launchCommand)
}

var launchCommand = &cobra.Command{
	Use:     "launch <instance-name> <image-alias-name>",
	Short:   "Launch a lxc instance with specification",
	Long:    `Launch a lxc instance with specification`,
	Args:    cobra.ExactArgs(2),
	PreRunE: validateLaunch,
	RunE:    handleLaunch,
}

func validateLaunch(cmd *cobra.Command, args []string) error {
	var err error
	if len(args) < 2 {
		return errors.New("require instance name and image alias name")
	}
	instName = args[0]
	lxcImage = args[1]
	if (len(lxdSocket) == 0 || !fileutil.Exist(lxdSocket)) && len(lxdServerAddress) == 0 {
		return errors.New(fmt.Sprintf("lxd socket file %s not existed and lxd server address %s not specified",
			lxdSocket, lxdServerAddress))
	}

	if instType != InstanceVM && instType != InstanceContainer {
		return errors.New("lxd only accepts virtual machine or container type")
	}

	if lxdClient, err = lxd.NewClient(lxdSocket, lxdServerAddress, clientKeyPath, clientCertPath, log.Logger); err != nil {
		return err
	}

	log.Logger.Info(fmt.Sprintf("start to validate resource limit on instance %s", instName))
	if err = lxdClient.ValidateResourceLimit(
		egressLimit, ingressLimit, rootSize, storagePool, memoryResource, cpuResource, additionalConfig,
		networkDeviceName); err != nil {
		return err
	}
	log.Logger.Info(fmt.Sprintf("start to check image %s existence", lxcImage))
	imageExists, err := lxdClient.CheckImageByAlias(lxcImage)
	if err != nil {
		return err
	}
	if !imageExists {
		return errors.New(fmt.Sprintf("unable to find image by alias %s", lxcImage))
	}
	log.Logger.Info(fmt.Sprintf("start to check instance %s existence", instName))
	instanceExists, err := lxdClient.CheckInstanceExists(instName, instType)
	if err != nil {
		return err
	}
	log.Logger.Info(fmt.Sprintf("start to check storage pool %s existence", storagePool))
	if len(storagePool) == 0 {
		return errors.New("please specify storage pool name")
	}
	if existed, err := lxdClient.CheckPoolExists(storagePool); err != nil || !existed {
		if err != nil {
			return err
		}
		return errors.New(fmt.Sprintf("storage pool %s not existed", storagePool))
	}
	if instanceExists && removeExisting {
		log.Logger.Info(fmt.Sprintf("start to remove lxc instance %s due to existence", instName))
		err = lxdClient.StopInstance(instName, true)
		if err != nil {
			return err
		}
	}

	return nil
}

func createInstance(cmd *cobra.Command, args []string) error {
	instanceExists, err := lxdClient.CheckInstanceExists(instName, instType)
	if err != nil {
		return err
	}
	if !instanceExists {
		//launch instance
		log.Logger.Info(fmt.Sprintf("start to create instance %s", instName))
		err = lxdClient.CreateInstance(lxcImage, instName, instProfiles, instType)
		if err != nil {
			return err
		}
	}
	log.Logger.Info(fmt.Sprintf("start to launch instance %s", instName))
	err = lxdClient.LaunchInstance(instName, instEnvs, startCommand, networkDeviceName, NetworkMaxWaitTime)
	if err != nil {
		return err
	}
	return nil
}

func CleanupLaunch() {
	if networkProxy != nil {
		networkProxy.Close()
	}
	if prober != nil {
		prober.Close()
	}
	if statusServer != nil {
		statusServer.Close()
	}
	if len(instName) != 0 && lxdClient != nil {
		if err := lxdClient.StopInstance(instName, true); err != nil {
			log.Logger.Error(fmt.Sprintf("failed to clean up lxd instance %s, %s", instName, err))
		}
	}
	time.Sleep(10 * time.Second)
}

func launchStatusHandler(w http.ResponseWriter, req *http.Request) {
	if prober.Alive() {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf("instance %s alive", instName)))
	} else {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("instance %s go dead", instName)))
	}
}

func handleLaunch(cmd *cobra.Command, args []string) error {
	var err error
	var ipaddress string
	// create and wait instance ready
	if err = createInstance(cmd, args); err != nil {
		return err
	}
	//start proxy if needed
	if proxyPort != 0 {
		ipaddress, err = lxdClient.WaitInstanceNetworkReady(instName, networkDeviceName, NetworkMaxWaitTime)
		if err != nil {
			CleanupLaunch()
			return err
		}
		networkProxy, err = network.NewProxy(instName, "0.0.0.0", exposePort,
			fmt.Sprintf("%s:%d", ipaddress, proxyPort), log.Logger)
		if err != nil {
			CleanupLaunch()
			return err
		}
		//watch instance status
		prober, err = task.NewProber(instName, lxdClient, 5, log.Logger)
		if err != nil {
			CleanupLaunch()
			return err
		}
		// start health status
		go prober.StartLoop()
		go util.ServerHealth(launchStatusHandler, statusPort)
		//watch os signal
		util.ListenSignals(CleanupLaunch)
		//start proxying
		networkProxy.StartLoop()
	}
	return nil
}
