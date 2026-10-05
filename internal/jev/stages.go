package jev

// The vocabulary of the decision graph (plan.md §30).
//
// These constants used to live inside an unreached 2155-line subsystem. They
// are extracted here because they are the contract between the orchestrator,
// the cache key and the trace: a decision stage written as a bare "D4" in one
// package and a bare "D4" in another is a typo waiting to lose an audit entry.
//
// The set of stages is fixed. A decision that does not belong to one of these
// ten does not belong in the graph.

// Decision stage names, in dependency order.
const (
	StageLexical      = "D0" // lexical segmentation
	StageSyntactic    = "D1" // syntactic ambiguities, including scope readings
	StagePredicate    = "D2" // predicate senses
	StageRoles        = "D3" // semantic roles
	StageCoref        = "D4" // coreference and zero anaphora
	StageDiscourse    = "D5" // discourse interpretation
	StagePragmatic    = "D6" // pragmatic interpretation
	StageProjection   = "D7" // target projection choices
	StageConstruction = "D8" // construction selection
	StageRanking      = "D9" // final ranking
)

// Stages is the dependency order of the graph.
var Stages = []string{
	StageLexical, StageSyntactic, StagePredicate, StageRoles, StageCoref,
	StageDiscourse, StagePragmatic, StageProjection, StageConstruction,
	StageRanking,
}

// StageName returns the human-readable name of a decision stage, or the stage
// id itself when it is not one of the ten.
func StageName(stage string) string {
	switch stage {
	case StageLexical:
		return "lexical segmentation"
	case StageSyntactic:
		return "syntactic ambiguity"
	case StagePredicate:
		return "predicate sense"
	case StageRoles:
		return "semantic role"
	case StageCoref:
		return "coreference"
	case StageDiscourse:
		return "discourse"
	case StagePragmatic:
		return "pragmatics"
	case StageProjection:
		return "target projection"
	case StageConstruction:
		return "construction selection"
	case StageRanking:
		return "final ranking"
	}
	return stage
}

// Decision actions: what a decision commits to in the JLIR. A decision that
// commits to nothing is a diagnostic, not a decision, and is recorded as such.
const (
	ActionSegmentation = "segmentation"
	ActionScope        = "scope"
	ActionReading      = "reading"
	ActionSense        = "sense"
	ActionRole         = "role"
	ActionReferent     = "referent"
	ActionPragmatic    = "pragmatic"
	ActionProjection   = "projection"
	ActionConstruction = "construction"
	ActionRanking      = "ranking"
	// ActionNone marks a question that is recorded in the audit trail but
	// commits to nothing, such as the D5 discourse probe.
	ActionNone = ""
)

// ladderMaxOptions is the width at which a candidate set stops being a single
// question and becomes a ladder walk (plan.md §33). It sits well below the 255
// option ceiling, because a 200-way choice is not a question anyone answers
// well; a model that must pick one of 200 options is being asked a question it
// cannot answer, and the probability it returns will be noise.
const ladderMaxOptions = 32

// maxOptions is the protocol ceiling the API enforces on a choice question.
const maxOptions = 255

// minScoreLevels and maxScoreLevels are the protocol bounds on a score scale.
const (
	minScoreLevels = 2
	maxScoreLevels = 10
)
