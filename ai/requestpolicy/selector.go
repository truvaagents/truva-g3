package requestpolicy

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/truvaagents/truva-g3/core"
)

const maxModelSelectorBytes = 256

type compiledPatch struct {
	patch        core.AIProviderPatch
	modelPattern *regexp.Regexp
}

func compileSelector(selector core.AIProviderSelector) (*regexp.Regexp, error) {
	scoped := selector.Provider != "" ||
		selector.ProviderAlias != "" ||
		selector.Surface != "" ||
		selector.Model != ""
	if !scoped && !selector.AllProviders {
		return nil, errors.New("request rule requires provider, alias, surface, or model; set AllProviders explicitly for a global rule")
	}
	if selector.Model != "" {
		return compileModelGlob(selector.Model)
	}
	return nil, nil
}

func compileModelGlob(pattern string) (*regexp.Regexp, error) {
	if len(pattern) > maxModelSelectorBytes {
		return nil, fmt.Errorf("model selector exceeds %d bytes", maxModelSelectorBytes)
	}
	quoted := regexp.QuoteMeta(strings.ToLower(pattern))
	expression := "^" + strings.ReplaceAll(quoted, `\*`, ".*") + "$"
	return regexp.Compile(expression)
}

func matches(selector core.AIProviderSelector, modelPattern *regexp.Regexp, info RequestInfo) bool {
	if selector.Provider != "" && !strings.EqualFold(selector.Provider, info.Provider) {
		return false
	}
	if selector.ProviderAlias != "" && !strings.EqualFold(selector.ProviderAlias, info.ProviderAlias) {
		return false
	}
	if selector.Surface != "" && !strings.EqualFold(selector.Surface, info.Surface) {
		return false
	}
	if selector.Operation != "" && !strings.EqualFold(selector.Operation, info.Operation) {
		return false
	}
	if selector.Purpose != "" && selector.Purpose != info.Purpose {
		return false
	}
	if selector.Model != "" {
		if modelPattern == nil || !modelPattern.MatchString(strings.ToLower(strings.TrimSpace(info.ResolvedModel))) {
			return false
		}
	}
	return true
}

func validatePatch(patch core.AIProviderPatch) (*regexp.Regexp, error) {
	if strings.TrimSpace(patch.Name) == "" {
		return nil, errors.New("request rule name is required")
	}
	if strings.TrimSpace(patch.Version) == "" {
		return nil, errors.New("request rule version is required")
	}
	modelPattern, err := compileSelector(patch.Selector)
	if err != nil {
		return nil, err
	}

	setPaths := make(map[string]struct{}, len(patch.Set))
	for _, path := range sortedMapKeys(patch.Set) {
		tokens, err := parsePointer(path)
		if err != nil {
			return nil, fmt.Errorf("set path %q: %w", path, err)
		}
		if containsAppendToken(tokens) {
			return nil, fmt.Errorf("set path %q: array append token '-' is not supported", path)
		}
		setPaths[path] = struct{}{}
	}
	seenRemove := make(map[string]struct{}, len(patch.Remove))
	for _, path := range patch.Remove {
		tokens, err := parsePointer(path)
		if err != nil {
			return nil, fmt.Errorf("remove path %q: %w", path, err)
		}
		if containsAppendToken(tokens) {
			return nil, fmt.Errorf("remove path %q: array append token '-' is not supported", path)
		}
		if _, duplicate := seenRemove[path]; duplicate {
			return nil, fmt.Errorf("remove path %q is duplicated", path)
		}
		seenRemove[path] = struct{}{}
		if _, ambiguous := setPaths[path]; ambiguous {
			return nil, fmt.Errorf("path %q cannot appear in both set and remove", path)
		}
	}

	seenHeaders := make(map[string]struct{}, len(patch.SetHeaders))
	for _, name := range sortedMapKeys(patch.SetHeaders) {
		value := patch.SetHeaders[name]
		if err := validateHeader(name, value); err != nil {
			return nil, err
		}
		canonical := strings.ToLower(name)
		if _, duplicate := seenHeaders[canonical]; duplicate {
			return nil, fmt.Errorf("header %q is set more than once with different casing", name)
		}
		seenHeaders[canonical] = struct{}{}
	}
	seenRemoveHeaders := make(map[string]struct{}, len(patch.RemoveHeaders))
	for _, name := range patch.RemoveHeaders {
		if err := validateHeaderName(name); err != nil {
			return nil, err
		}
		canonical := strings.ToLower(name)
		if _, duplicate := seenRemoveHeaders[canonical]; duplicate {
			return nil, fmt.Errorf("remove header %q is duplicated", name)
		}
		seenRemoveHeaders[canonical] = struct{}{}
		if _, ambiguous := seenHeaders[canonical]; ambiguous {
			return nil, fmt.Errorf("header %q cannot appear in both set and remove", name)
		}
	}
	return modelPattern, nil
}

func containsAppendToken(tokens []string) bool {
	for _, token := range tokens {
		if token == "-" {
			return true
		}
	}
	return false
}

func cloneAndValidatePatches(patches []core.AIProviderPatch) ([]compiledPatch, error) {
	if patches == nil {
		return nil, nil
	}
	cloned := make([]compiledPatch, len(patches))
	for index, patch := range patches {
		modelPattern, err := validatePatch(patch)
		if err != nil {
			return nil, fmt.Errorf("patch %q: %w", patch.Name, err)
		}
		cloned[index] = compiledPatch{patch: patch, modelPattern: modelPattern}
		cloned[index].patch.Remove = append([]string(nil), patch.Remove...)
		cloned[index].patch.SetHeaders = cloneStringMap(patch.SetHeaders)
		cloned[index].patch.RemoveHeaders = append([]string(nil), patch.RemoveHeaders...)
		if patch.Set != nil {
			cloned[index].patch.Set = make(map[string]interface{}, len(patch.Set))
			paths := sortedMapKeys(patch.Set)
			for _, path := range paths {
				value, err := CloneJSONValue(patch.Set[path])
				if err != nil {
					return nil, fmt.Errorf("patch %q path %q: %w", patch.Name, path, err)
				}
				cloned[index].patch.Set[path] = value
			}
		}
	}
	return cloned, nil
}

// ClonePatches validates patches and returns an isolated snapshot suitable for
// retaining in application or provider configuration.
func ClonePatches(patches []core.AIProviderPatch) ([]core.AIProviderPatch, error) {
	compiled, err := cloneAndValidatePatches(patches)
	if err != nil {
		return nil, err
	}
	if compiled == nil {
		return nil, nil
	}
	cloned := make([]core.AIProviderPatch, len(compiled))
	for index := range compiled {
		cloned[index] = compiled[index].patch
	}
	return cloned, nil
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func sortedMapKeys[V any](source map[string]V) []string {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
