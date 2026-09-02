package main

import (
	"flag"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
)

// configFlags holds the three flags runController and runManifests both
// declare, registered on the caller's own FlagSet so each keeps its own
// help text and exit behavior. Registration must happen before the
// caller's Parse; build must happen after, since it needs the parsed
// --node-selector value.
type configFlags struct {
	namespace            string
	nodeSelectorFlag     string
	writeLegacyEndpoints bool
}

// registerConfigFlags registers the shared flags on fs, to be called
// before fs is parsed (fs.Parse for manifests' own FlagSet, flag.Parse
// for the top-level flag.CommandLine).
func registerConfigFlags(fs *flag.FlagSet, namespaceHelp, nodeSelectorHelp, legacyEndpointsHelp string) *configFlags {
	c := &configFlags{}
	fs.StringVar(&c.namespace, "namespace", "kube-system", namespaceHelp)
	fs.StringVar(&c.nodeSelectorFlag, "node-selector", config.ControlPlaneNodeSelector+"=true", nodeSelectorHelp)
	fs.BoolVar(&c.writeLegacyEndpoints, "write-legacy-endpoints", false, legacyEndpointsHelp)
	return c
}

// build parses --node-selector and assembles a config.Config, to be
// called after fs has been parsed.
func (c *configFlags) build() (*config.Config, error) {
	nodeSelector, err := parseSelector(c.nodeSelectorFlag)
	if err != nil {
		return nil, err
	}
	return &config.Config{
		Namespace:            c.namespace,
		NodeSelector:         nodeSelector,
		WriteLegacyEndpoints: c.writeLegacyEndpoints,
		Services:             config.DefaultServices,
	}, nil
}
