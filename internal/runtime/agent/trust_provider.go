package agent

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// UnverifiedTrustProvider tells the model that the instance it is
// running inside could not verify its own configuration.
//
// The withheld tools already produce an error when reached, but an
// error at the moment of use is late: by then the model has committed
// to a plan that assumed it could reach someone. Stating the condition
// up front lets it choose differently — finish the analysis, put the
// findings in its reply, and say plainly that the instance needs
// attention.
//
// It also matters for judgment rather than capability. An instance that
// cannot show who authorized its configuration should be more reluctant
// about consequential action generally, not only about the specific
// tools that happen to be blocked.
type UnverifiedTrustProvider struct {
	configPath string
}

// NewUnverifiedTrustProvider builds the provider for an instance running
// on an unverified config.
func NewUnverifiedTrustProvider(configPath string) *UnverifiedTrustProvider {
	return &UnverifiedTrustProvider{configPath: configPath}
}

// TagContextBucket puts this in live state: it describes the current
// operating condition of the instance, and it changes only on restart.
func (p *UnverifiedTrustProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketLiveState
}

// TagContext renders the trust notice.
func (p *UnverifiedTrustProvider) TagContext(context.Context, agentctx.ContextRequest) (string, error) {
	if p == nil {
		return "", nil
	}
	path := p.configPath
	if path == "" {
		path = "an unspecified path"
	}
	return "## Instance Trust\n\n" +
		"This instance is running on a configuration loaded from outside its trust boundary (" + path + "), " +
		"so there is no signed record of who authorized it. Two capabilities are withheld while that is true: " +
		"tools that contact a human directly, and the automatic start of service loops.\n\n" +
		"Work normally otherwise, but do not plan around reaching someone directly — put what you would have sent " +
		"into your reply instead, and say that the instance needs to be restarted on a verified configuration. " +
		"Treat consequential or irreversible actions with more caution than usual: the configuration that decides " +
		"what you are permitted to do is itself unattested.", nil
}
