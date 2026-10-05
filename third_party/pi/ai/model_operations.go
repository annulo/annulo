package ai

// Model types, ported from pi utils/model-operations.ts (upstream a328aa89a).
// pi publishes these through its "./utils/*" export, and the coding agent
// imports assertChatModel from there, which is why AssertChatModel is exported
// here. The image and classifier halves (assertImageModel,
// assertClassifierModel, imageErrorResult, classifierErrorResult) arrive with
// the non-chat model operations they serve.

// ModelType is the operation a model serves (pi ModelType). A model whose Type
// is empty is a chat model: see GetModelType.
type ModelType string

const (
	ModelTypeChat       ModelType = "chat"
	ModelTypeImage      ModelType = "image"
	ModelTypeClassifier ModelType = "classifier"
)

// GetModelType returns a model's type, treating a model without one as a chat
// model (pi getModelType: `model.type ?? "chat"`).
func GetModelType(m *Model) ModelType {
	if m.Type == "" {
		return ModelTypeChat
	}
	return m.Type
}

// IsModelType reports whether a model is of type t, counting a model without a
// type as a chat model (pi isModelType).
func IsModelType(m *Model, t ModelType) bool {
	return GetModelType(m) == t
}

// AssertChatModel fails unless the model is a chat model (pi assertChatModel).
// Every chat entry point asserts it before any provider lookup, so a model of
// another type fails the same way whether or not its provider is known.
func AssertChatModel(m *Model) error {
	if !IsModelType(m, ModelTypeChat) {
		return newModelsError(ErrProvider, "Model "+m.Provider+"/"+m.ID+" is not a chat model", nil)
	}
	return nil
}

// hasKnownModelType reports whether this version knows a model's type (pi
// hasKnownModelType). Models from stores and remote sources may carry types
// only newer versions know. pi checks membership with Object.hasOwn so that an
// inherited name such as "toString" is not a known type; a switch has no such
// trap.
func hasKnownModelType(m *Model) bool {
	switch GetModelType(m) {
	case ModelTypeChat, ModelTypeImage, ModelTypeClassifier:
		return true
	}
	return false
}

// knownTypeModels returns the models whose type this version knows, as a new
// slice.
func knownTypeModels(models []*Model) []*Model {
	out := make([]*Model, 0, len(models))
	for _, m := range models {
		if hasKnownModelType(m) {
			out = append(out, m)
		}
	}
	return out
}

// withKnownModelTypes drops stored models whose type this version does not know
// (pi withKnownModelTypes), returning a copy of the entry; nil stays nil.
func withKnownModelTypes(entry *ModelsStoreEntry) *ModelsStoreEntry {
	if entry == nil {
		return nil
	}
	out := *entry
	out.Models = knownTypeModels(entry.Models)
	return &out
}
