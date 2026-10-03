package parser

import (
	"strings"
)

// findJunieStream matches an event target identifier or stepId to an existing
// subagent stream in streams.
func findJunieStream(
	streams map[string]*junieAgentStream,
	targetAgent, stepID string,
) *junieAgentStream {
	if targetAgent == "main" {
		return streams["main"]
	}
	cleanStep := strings.TrimSuffix(strings.TrimSuffix(stepID, "-request"), "-spawned")
	// 1. Exact match on targetAgent as stream ID
	if targetAgent != "" {
		if st := streams[targetAgent]; st != nil {
			return st
		}
	}
	// 2. Exact match on stepID as stream stepID
	if cleanStep != "" {
		for _, st := range streams {
			if st.id != "main" && (st.stepID == cleanStep || st.stepID == stepID) {
				return st
			}
		}
	}
	// 3. Match by name or taskName
	if targetAgent != "" {
		for _, st := range streams {
			if st.id != "main" && (st.name == targetAgent || st.taskName == targetAgent) {
				return st
			}
		}
		// Match targetAgent against stream stepID
		for _, st := range streams {
			if st.id != "main" && (st.stepID == targetAgent || strings.TrimSuffix(targetAgent, "-request") == st.stepID) {
				return st
			}
		}
	}
	// 4. Check if stepID is in any stream's blocks
	if stepID != "" {
		for _, st := range streams {
			if st.id != "main" {
				if _, ok := st.blocks[stepID]; ok {
					return st
				}
			}
		}
	}
	return nil
}
