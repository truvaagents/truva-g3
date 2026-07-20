package requestpolicy

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

// DocumentConfig describes an isolated provider-local logical request. Body is
// adopted by the document; callers must pass call-local state.
type DocumentConfig struct {
	Info                 RequestInfo
	Body                 map[string]interface{}
	Headers              map[string]string
	ProtectedPaths       []string
	ProtectedHeaders     []string
	CaseInsensitivePaths []string
}

// Document implements the common JSON Pointer and header mutation semantics
// used by provider drafts.
type Document struct {
	info                 RequestInfo
	body                 map[string]interface{}
	headers              map[string]string
	protectedPaths       []string
	protectedHeaders     map[string]struct{}
	caseInsensitivePaths map[string]struct{}
}

// NewDocument adopts an isolated body and snapshots all other configuration.
func NewDocument(config DocumentConfig) (*Document, error) {
	if config.Body == nil {
		config.Body = make(map[string]interface{})
	}
	document := &Document{
		info:                 config.Info,
		body:                 config.Body,
		headers:              make(map[string]string, len(config.Headers)),
		protectedPaths:       append([]string(nil), config.ProtectedPaths...),
		protectedHeaders:     make(map[string]struct{}, len(config.ProtectedHeaders)),
		caseInsensitivePaths: make(map[string]struct{}, len(config.CaseInsensitivePaths)),
	}
	for _, path := range document.protectedPaths {
		if _, err := parsePointer(path); err != nil {
			return nil, fmt.Errorf("protected path %q: %w", path, err)
		}
	}
	for _, path := range config.CaseInsensitivePaths {
		tokens, err := parsePointer(path)
		if err != nil {
			return nil, fmt.Errorf("case-insensitive path %q: %w", path, err)
		}
		if len(tokens) != 1 {
			return nil, fmt.Errorf("case-insensitive path %q must address a top-level member", path)
		}
		document.caseInsensitivePaths[path] = struct{}{}
	}
	for _, name := range config.ProtectedHeaders {
		if err := validateHeaderName(name); err != nil {
			return nil, fmt.Errorf("protected header: %w", err)
		}
		document.protectedHeaders[strings.ToLower(name)] = struct{}{}
	}
	seenHeaders := make(map[string]struct{}, len(config.Headers))
	for _, name := range sortedMapKeys(config.Headers) {
		canonical := strings.ToLower(name)
		if _, duplicate := seenHeaders[canonical]; duplicate {
			return nil, fmt.Errorf("initial header %q is duplicated with different casing", name)
		}
		seenHeaders[canonical] = struct{}{}
		value := config.Headers[name]
		if err := document.SetHeader(name, value); err != nil {
			return nil, fmt.Errorf("initial header: %w", err)
		}
	}
	return document, nil
}

// Info returns the immutable request identity.
func (d *Document) Info() RequestInfo { return d.info }

// Get reads a logical body value. Callers outside the engine should not mutate
// returned containers; middleware receives a defensive copy from the editor.
func (d *Document) Get(path string) (interface{}, bool) {
	tokens, err := parsePointer(path)
	if err != nil {
		return nil, false
	}
	if d.isCaseInsensitive(path) {
		for key, value := range d.body {
			if strings.EqualFold(key, tokens[0]) {
				return value, true
			}
		}
		return nil, false
	}
	return getPointer(d.body, tokens)
}

// WouldSetChange reports whether Set would alter the logical document. It lets
// the tracking editor account for case-folded duplicate legacy fields without
// exposing provider-specific representation details.
func (d *Document) WouldSetChange(path string, value interface{}) bool {
	if d.isCaseInsensitive(path) {
		tokens, err := parsePointer(path)
		if err != nil {
			return true
		}
		matches := 0
		var existing interface{}
		for key, candidate := range d.body {
			if strings.EqualFold(key, tokens[0]) {
				matches++
				existing = candidate
			}
		}
		return matches != 1 || !reflect.DeepEqual(existing, value)
	}
	existing, exists := d.Get(path)
	return !exists || !reflect.DeepEqual(existing, value)
}

// Set adds or replaces a logical body value.
func (d *Document) Set(path string, value interface{}) error {
	if err := d.checkPath(path); err != nil {
		return err
	}
	tokens, _ := parsePointer(path)
	if d.isCaseInsensitive(path) {
		for key := range d.body {
			if strings.EqualFold(key, tokens[0]) {
				delete(d.body, key)
			}
		}
		d.body[tokens[0]] = value
		return nil
	}
	updated, err := setPointer(d.body, tokens, value)
	if err != nil {
		return err
	}
	body, ok := updated.(map[string]interface{})
	if !ok {
		return errors.New("logical request root must remain an object")
	}
	d.body = body
	return nil
}

// Remove deletes a logical body value. Removing a missing path is a no-op.
func (d *Document) Remove(path string) error {
	if err := d.checkPath(path); err != nil {
		return err
	}
	tokens, _ := parsePointer(path)
	if d.isCaseInsensitive(path) {
		for key := range d.body {
			if strings.EqualFold(key, tokens[0]) {
				delete(d.body, key)
			}
		}
		return nil
	}
	updated, _, err := removePointer(d.body, tokens)
	if err != nil {
		return err
	}
	body, ok := updated.(map[string]interface{})
	if !ok {
		return errors.New("logical request root must remain an object")
	}
	d.body = body
	return nil
}

// SetHeader adds or replaces an eligible header using case-insensitive names.
func (d *Document) SetHeader(name, value string) error {
	if err := validateHeader(name, value); err != nil {
		return err
	}
	if _, protected := d.protectedHeaders[strings.ToLower(name)]; protected {
		return fmt.Errorf("header %q is protected", name)
	}
	canonical := http.CanonicalHeaderKey(name)
	for existing := range d.headers {
		if strings.EqualFold(existing, name) && existing != canonical {
			delete(d.headers, existing)
		}
	}
	d.headers[canonical] = value
	return nil
}

// RemoveHeader removes an eligible header. Missing headers are a no-op.
func (d *Document) RemoveHeader(name string) error {
	if err := validateHeaderName(name); err != nil {
		return err
	}
	if _, protected := d.protectedHeaders[strings.ToLower(name)]; protected {
		return fmt.Errorf("header %q is protected", name)
	}
	for existing := range d.headers {
		if strings.EqualFold(existing, name) {
			delete(d.headers, existing)
		}
	}
	return nil
}

// Header reads an eligible header case-insensitively.
func (d *Document) Header(name string) (string, bool) {
	for existing, value := range d.headers {
		if strings.EqualFold(existing, name) {
			return value, true
		}
	}
	return "", false
}

// Body returns the document-owned logical body for provider validation and
// encoding after policy application.
func (d *Document) Body() map[string]interface{} { return d.body }

// Headers returns an isolated snapshot of eligible logical headers.
func (d *Document) Headers() map[string]string { return cloneStringMap(d.headers) }

// Validate satisfies Draft. Provider wrappers should add surface invariants.
func (d *Document) Validate() error { return nil }

func (d *Document) checkPath(path string) error {
	if _, err := parsePointer(path); err != nil {
		return err
	}
	for _, protected := range d.protectedPaths {
		if path == protected || strings.HasPrefix(path, protected+"/") || strings.HasPrefix(protected, path+"/") {
			return fmt.Errorf("path %q is protected", path)
		}
	}
	return nil
}

func (d *Document) isCaseInsensitive(path string) bool {
	_, ok := d.caseInsensitivePaths[path]
	return ok
}

func parsePointer(path string) ([]string, error) {
	if path == "" {
		return nil, errors.New("empty JSON Pointer root is not supported")
	}
	if !strings.HasPrefix(path, "/") {
		return nil, errors.New("JSON Pointer must begin with '/'")
	}
	raw := strings.Split(path[1:], "/")
	tokens := make([]string, len(raw))
	for index, token := range raw {
		var builder strings.Builder
		for position := 0; position < len(token); position++ {
			if token[position] != '~' {
				builder.WriteByte(token[position])
				continue
			}
			if position+1 >= len(token) {
				return nil, errors.New("invalid JSON Pointer escape")
			}
			position++
			switch token[position] {
			case '0':
				builder.WriteByte('~')
			case '1':
				builder.WriteByte('/')
			default:
				return nil, errors.New("invalid JSON Pointer escape")
			}
		}
		tokens[index] = builder.String()
	}
	return tokens, nil
}

func getPointer(current interface{}, tokens []string) (interface{}, bool) {
	if len(tokens) == 0 {
		return current, true
	}
	value := reflect.ValueOf(current)
	for value.IsValid() && value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil, false
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return nil, false
	}
	switch value.Kind() {
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String || value.IsNil() {
			return nil, false
		}
		key := reflect.ValueOf(tokens[0]).Convert(value.Type().Key())
		child := value.MapIndex(key)
		if !child.IsValid() {
			return nil, false
		}
		return getPointer(child.Interface(), tokens[1:])
	case reflect.Slice, reflect.Array:
		index, ok := existingIndex(tokens[0], value.Len())
		if !ok {
			return nil, false
		}
		return getPointer(value.Index(index).Interface(), tokens[1:])
	default:
		return nil, false
	}
}

func setPointer(current interface{}, tokens []string, replacement interface{}) (interface{}, error) {
	if len(tokens) == 0 {
		return replacement, nil
	}
	value := reflect.ValueOf(current)
	for value.IsValid() && value.Kind() == reflect.Interface {
		if value.IsNil() {
			value = reflect.Value{}
			break
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		value = reflect.ValueOf(map[string]interface{}{})
	}

	switch value.Kind() {
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("cannot traverse map with key type %s", value.Type().Key())
		}
		if value.IsNil() {
			value = reflect.MakeMap(value.Type())
		}
		key := reflect.ValueOf(tokens[0]).Convert(value.Type().Key())
		if len(tokens) == 1 {
			assigned, err := assignableValue(replacement, value.Type().Elem())
			if err != nil {
				return nil, err
			}
			value.SetMapIndex(key, assigned)
			return value.Interface(), nil
		}
		child := value.MapIndex(key)
		var childValue interface{}
		if child.IsValid() {
			childValue = child.Interface()
		} else if value.Type().Elem().Kind() == reflect.Interface {
			childValue = map[string]interface{}{}
		} else {
			return nil, fmt.Errorf("cannot create object parent %q in map element type %s", tokens[0], value.Type().Elem())
		}
		updated, err := setPointer(childValue, tokens[1:], replacement)
		if err != nil {
			return nil, err
		}
		assigned, err := assignableValue(updated, value.Type().Elem())
		if err != nil {
			return nil, err
		}
		value.SetMapIndex(key, assigned)
		return value.Interface(), nil
	case reflect.Slice, reflect.Array:
		index, ok := existingIndex(tokens[0], value.Len())
		if !ok {
			return nil, fmt.Errorf("array index %q does not identify an existing element", tokens[0])
		}
		if value.Kind() == reflect.Array {
			clone := reflect.New(value.Type()).Elem()
			clone.Set(value)
			value = clone
		}
		if len(tokens) == 1 {
			assigned, err := assignableValue(replacement, value.Type().Elem())
			if err != nil {
				return nil, err
			}
			value.Index(index).Set(assigned)
			return value.Interface(), nil
		}
		updated, err := setPointer(value.Index(index).Interface(), tokens[1:], replacement)
		if err != nil {
			return nil, err
		}
		assigned, err := assignableValue(updated, value.Type().Elem())
		if err != nil {
			return nil, err
		}
		value.Index(index).Set(assigned)
		return value.Interface(), nil
	default:
		return nil, fmt.Errorf("cannot traverse %s value", value.Kind())
	}
}

func removePointer(current interface{}, tokens []string) (interface{}, bool, error) {
	value := reflect.ValueOf(current)
	for value.IsValid() && value.Kind() == reflect.Interface {
		if value.IsNil() {
			return current, false, nil
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return current, false, nil
	}

	switch value.Kind() {
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String || value.IsNil() {
			return current, false, nil
		}
		key := reflect.ValueOf(tokens[0]).Convert(value.Type().Key())
		child := value.MapIndex(key)
		if !child.IsValid() {
			return current, false, nil
		}
		if len(tokens) == 1 {
			value.SetMapIndex(key, reflect.Value{})
			return value.Interface(), true, nil
		}
		updated, removed, err := removePointer(child.Interface(), tokens[1:])
		if err != nil || !removed {
			return current, removed, err
		}
		assigned, err := assignableValue(updated, value.Type().Elem())
		if err != nil {
			return nil, false, err
		}
		value.SetMapIndex(key, assigned)
		return value.Interface(), true, nil
	case reflect.Slice:
		index, ok := existingIndex(tokens[0], value.Len())
		if !ok {
			return current, false, nil
		}
		if len(tokens) == 1 {
			clone := reflect.MakeSlice(value.Type(), value.Len()-1, value.Len()-1)
			reflect.Copy(clone.Slice(0, index), value.Slice(0, index))
			reflect.Copy(clone.Slice(index, clone.Len()), value.Slice(index+1, value.Len()))
			return clone.Interface(), true, nil
		}
		updated, removed, err := removePointer(value.Index(index).Interface(), tokens[1:])
		if err != nil || !removed {
			return current, removed, err
		}
		assigned, err := assignableValue(updated, value.Type().Elem())
		if err != nil {
			return nil, false, err
		}
		value.Index(index).Set(assigned)
		return value.Interface(), true, nil
	case reflect.Array:
		index, ok := existingIndex(tokens[0], value.Len())
		if !ok {
			return current, false, nil
		}
		if len(tokens) == 1 {
			return nil, false, errors.New("cannot remove an element from a fixed-length array")
		}
		clone := reflect.New(value.Type()).Elem()
		clone.Set(value)
		updated, removed, err := removePointer(clone.Index(index).Interface(), tokens[1:])
		if err != nil || !removed {
			return current, removed, err
		}
		assigned, err := assignableValue(updated, clone.Type().Elem())
		if err != nil {
			return nil, false, err
		}
		clone.Index(index).Set(assigned)
		return clone.Interface(), true, nil
	default:
		return current, false, nil
	}
}

func existingIndex(token string, length int) (int, bool) {
	if token == "-" || token == "" {
		return 0, false
	}
	if len(token) > 1 && token[0] == '0' {
		return 0, false
	}
	index, err := strconv.Atoi(token)
	return index, err == nil && index >= 0 && index < length
}

func assignableValue(value interface{}, target reflect.Type) (reflect.Value, error) {
	if value == nil {
		switch target.Kind() {
		case reflect.Interface, reflect.Map, reflect.Slice, reflect.Pointer:
			return reflect.Zero(target), nil
		default:
			return reflect.Value{}, fmt.Errorf("null is not assignable to %s", target)
		}
	}
	reflected := reflect.ValueOf(value)
	if reflected.Type().AssignableTo(target) {
		return reflected, nil
	}
	if target.Kind() == reflect.Interface && reflected.Type().Implements(target) {
		return reflected, nil
	}
	return reflect.Value{}, fmt.Errorf("value type %s is not assignable to %s", reflected.Type(), target)
}

func validateHeader(name, value string) error {
	if err := validateHeaderName(name); err != nil {
		return err
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == 0x7f || character < 0x20 && character != '\t' {
			return fmt.Errorf("header %q contains an invalid value", name)
		}
	}
	return nil
}

func validateHeaderName(name string) error {
	if name == "" {
		return errors.New("header name is empty")
	}
	for _, character := range name {
		if !isTokenCharacter(character) {
			return fmt.Errorf("header name %q is invalid", name)
		}
	}
	return nil
}

func isTokenCharacter(character rune) bool {
	if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", character)
}
