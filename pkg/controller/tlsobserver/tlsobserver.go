package tlsobserver

import (
	"context"
	"crypto/tls"
	"sync"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/crypto"
	"k8s.io/apimachinery/pkg/api/errors"
	k8sapiflag "k8s.io/component-base/cli/flag"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

const (
	// APIServerInstanceName is the singleton name for APIServer configuration
	APIServerInstanceName = "cluster"

	// DefaultResyncPeriod for the informer
	DefaultResyncPeriod = 10 * time.Minute
)

// TLSConfig holds the resolved TLS configuration
type TLSConfig struct {
	MinTLSVersion    string
	CipherSuites     []string // OpenSSL names
	CipherSuitesIANA []string // IANA names for Go
	Profile          *configv1.TLSSecurityProfile
}

// TLSChangeCallback is called when the TLS profile changes
type TLSChangeCallback func(newConfig *TLSConfig)

// TLSObserver watches for TLS profile changes on the APIServer and provides
// cached access to the current TLS configuration using an informer/lister pattern.
type TLSObserver struct {
	configInformers  configinformers.SharedInformerFactory
	apiServerLister  configlistersv1.APIServerLister
	hasSynced        cache.InformerSynced

	mu            sync.RWMutex
	currentConfig *TLSConfig
	callbacks     []TLSChangeCallback
}

// NewTLSObserver creates a new TLS observer that watches for APIServer TLS profile changes.
// It uses an informer/lister pattern for efficient, event-driven updates.
func NewTLSObserver(cfg *rest.Config) (*TLSObserver, error) {
	configClient, err := configclient.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	configInformers := configinformers.NewSharedInformerFactory(configClient, DefaultResyncPeriod)
	apiServerInformer := configInformers.Config().V1().APIServers()

	observer := &TLSObserver{
		configInformers: configInformers,
		apiServerLister: apiServerInformer.Lister(),
		hasSynced:       apiServerInformer.Informer().HasSynced,
		callbacks:       []TLSChangeCallback{},
	}

	// Set up event handlers for the informer
	_, err = apiServerInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { observer.handleAPIServerChange(obj) },
		UpdateFunc: func(oldObj, newObj interface{}) { observer.handleAPIServerChange(newObj) },
		DeleteFunc: func(obj interface{}) { observer.handleAPIServerChange(nil) },
	})
	if err != nil {
		return nil, err
	}

	klog.Info("TLS observer: created with informer/lister pattern for APIServer")
	return observer, nil
}

// Start starts the informer and waits for cache sync
func (o *TLSObserver) Start(ctx context.Context) error {
	klog.Info("TLS observer: starting config informers")
	o.configInformers.Start(ctx.Done())

	klog.Info("TLS observer: waiting for cache sync")
	if !cache.WaitForCacheSync(ctx.Done(), o.hasSynced) {
		return ctx.Err()
	}

	// Load initial configuration
	o.loadCurrentConfig()
	klog.InfoS("TLS observer: cache synced, initial TLS profile loaded",
		"profile", o.currentConfig.ProfileDescription(),
		"minTLSVersion", o.currentConfig.MinTLSVersion)

	return nil
}

// RegisterCallback registers a callback to be called when TLS profile changes
func (o *TLSObserver) RegisterCallback(cb TLSChangeCallback) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.callbacks = append(o.callbacks, cb)
}

// GetCurrentTLSConfig returns the current TLS configuration using the lister (cached)
func (o *TLSObserver) GetCurrentTLSConfig() *TLSConfig {
	o.mu.RLock()
	defer o.mu.RUnlock()

	if o.currentConfig != nil {
		return o.currentConfig
	}

	// Fallback to default if not yet synced
	return resolveTLSConfig(nil)
}

// GetGoTLSConfig returns a *tls.Config for use with HTTPS servers
func (o *TLSObserver) GetGoTLSConfig() (*tls.Config, error) {
	return o.GetCurrentTLSConfig().GetGoTLSConfig()
}

// handleAPIServerChange is called when the APIServer object changes
func (o *TLSObserver) handleAPIServerChange(obj interface{}) {
	o.loadCurrentConfig()

	// Notify callbacks
	o.mu.RLock()
	callbacks := make([]TLSChangeCallback, len(o.callbacks))
	copy(callbacks, o.callbacks)
	config := o.currentConfig
	o.mu.RUnlock()

	for _, cb := range callbacks {
		cb(config)
	}
}

// loadCurrentConfig loads the current TLS configuration from the lister
func (o *TLSObserver) loadCurrentConfig() {
	apiServer, err := o.apiServerLister.Get(APIServerInstanceName)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.V(4).Info("TLS observer: APIServer 'cluster' not found, using default Intermediate profile")
		} else {
			klog.Warningf("TLS observer: error fetching APIServer: %v", err)
		}
		o.mu.Lock()
		o.currentConfig = resolveTLSConfig(nil)
		o.mu.Unlock()
		return
	}

	newConfig := resolveTLSConfig(apiServer.Spec.TLSSecurityProfile)

	o.mu.Lock()
	oldProfile := ""
	if o.currentConfig != nil {
		oldProfile = o.currentConfig.ProfileDescription()
	}
	o.currentConfig = newConfig
	o.mu.Unlock()

	if oldProfile != "" && oldProfile != newConfig.ProfileDescription() {
		klog.InfoS("TLS observer: TLS profile changed",
			"from", oldProfile,
			"to", newConfig.ProfileDescription(),
			"minTLSVersion", newConfig.MinTLSVersion,
			"cipherSuites", len(newConfig.CipherSuites))
	}
}

// resolveTLSConfig resolves the effective TLS configuration from cluster profile.
// Falls back to Intermediate profile if clusterProfile is nil.
func resolveTLSConfig(clusterProfile *configv1.TLSSecurityProfile) *TLSConfig {
	minVersion, ciphers := getSecurityProfileCiphers(clusterProfile)
	ciphersIANA := crypto.OpenSSLToIANACipherSuites(ciphers)

	return &TLSConfig{
		MinTLSVersion:    minVersion,
		CipherSuites:     ciphers,
		CipherSuitesIANA: ciphersIANA,
		Profile:          clusterProfile,
	}
}

// getSecurityProfileCiphers extracts TLS settings from a profile.
// Returns Intermediate profile settings if profile is nil.
func getSecurityProfileCiphers(profile *configv1.TLSSecurityProfile) (string, []string) {
	if profile == nil {
		profileSpec := configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
		return string(profileSpec.MinTLSVersion), profileSpec.Ciphers
	}

	var profileSpec *configv1.TLSProfileSpec
	if profile.Type == configv1.TLSProfileCustomType {
		if profile.Custom != nil {
			profileSpec = &profile.Custom.TLSProfileSpec
		}
	} else {
		profileSpec = configv1.TLSProfiles[profile.Type]
	}

	// Fallback to Intermediate if nothing found
	if profileSpec == nil {
		profileSpec = configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	}

	return string(profileSpec.MinTLSVersion), profileSpec.Ciphers
}

// GetGoTLSConfig converts TLSConfig to a *tls.Config with explicit settings
// per OCPSTRAT-2611 (no reliance on Go defaults)
func (c *TLSConfig) GetGoTLSConfig() (*tls.Config, error) {
	defaultCiphers := configv1.TLSProfiles[configv1.TLSProfileIntermediateType].Ciphers

	minVersionID, err := k8sapiflag.TLSVersion(c.MinTLSVersion)
	if err != nil {
		klog.Warningf("Invalid TLS min version %s, using TLS 1.2: %v", c.MinTLSVersion, err)
		minVersionID = tls.VersionTLS12
	}

	cipherSuiteIDs, err := k8sapiflag.TLSCipherSuites(c.CipherSuitesIANA)
	if err != nil || len(cipherSuiteIDs) == 0 {
		klog.Warningf("Invalid cipher suites, using defaults: %v", err)
		cipherSuiteIDs, _ = k8sapiflag.TLSCipherSuites(
			crypto.OpenSSLToIANACipherSuites(defaultCiphers),
		)
	}

	config := &tls.Config{
		MinVersion:   minVersionID,
		CipherSuites: cipherSuiteIDs,
	}

	return config, nil
}

// ProfileDescription returns a human-readable description of the active profile
func (c *TLSConfig) ProfileDescription() string {
	if c.Profile == nil {
		return "Intermediate (default)"
	}
	return string(c.Profile.Type)
}
