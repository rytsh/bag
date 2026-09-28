package langs

import (
	"github.com/rytsh/bag/internal/model"
)

const maxParkedCallsPerNode = 64

// parkUnresolvedMemberCall keeps a member call whose receiver type is
// declared nowhere in the corpus on the caller node (metadata
// "unresolved_calls"), names only, so a merged multi-repo graph can finish
// the edge.
//
// Adapted from Graphify's _park_unresolved_member_call (Apache-2.0).
func parkUnresolvedMemberCall(caller *model.Node, callee, receiverType, lang string, rc *model.RawCall) {
	if caller == nil || callee == "" || receiverType == "" {
		return
	}

	if caller.Metadata == nil {
		caller.Metadata = map[string]any{}
	}

	parked, _ := caller.Metadata["unresolved_calls"].([]any)
	if len(parked) >= maxParkedCallsPerNode {
		return
	}

	for _, p := range parked {
		if m, ok := p.(model.OrderedFields); ok && m.Get("callee") == callee && m.Get("receiver_type") == receiverType {
			return
		}
	}

	entry := model.OrderedFields{{"callee", callee}, {"receiver_type", receiverType}, {"lang", lang}}
	if rc.SourceLocation != "" {
		entry = append(entry, [2]any{"line", rc.SourceLocation})
	}

	caller.Metadata["unresolved_calls"] = append(parked, entry)
}
