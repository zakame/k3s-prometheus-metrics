package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// envPrefix derives a flag's environment-variable fallback name, e.g.
// --leader-elect -> K3S_PROMETHEUS_METRICS_LEADER_ELECT.
const envPrefix = "K3S_PROMETHEUS_METRICS_"

// applyEnvDefaults pre-seeds fs's flags from environment variables named
// after envPrefix, before fs is parsed. An explicit command-line flag
// always wins over this, since Parse calls Set again for any flag it sees
// on argv, overwriting whatever Set did here first. A malformed env value
// errors out here unconditionally, even for a flag the command line goes
// on to override -- this runs before Parse, so it can't yet know that.
func applyEnvDefaults(fs *flag.FlagSet) error {
	var err error
	fs.VisitAll(func(f *flag.Flag) {
		if err != nil {
			return
		}
		name := envPrefix + strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))
		if v, ok := os.LookupEnv(name); ok {
			if setErr := fs.Set(f.Name, v); setErr != nil {
				err = fmt.Errorf("environment variable %s: %w", name, setErr)
			}
		}
	})
	return err
}
