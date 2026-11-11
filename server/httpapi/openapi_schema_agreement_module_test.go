// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// dtoSchemaException records a decision about a registered DTO's schema that
// its Go type cannot show. Required and Optional name top-level members;
// Nullable and NonNullable paths are dot-separated for nested members.
type dtoSchemaException struct {
	// Required members must be present although their pointer or Optional
	// type could observe absence: the handler rejects the omission.
	Required []string
	// Optional members may be absent although the DTO type would always carry
	// them: the handler defaults a missing plain request value, or the schema
	// leaves a response member that success always fills out of required.
	Optional []string
	// Nullable members deliberately carry JSON null where the type implies
	// they never do.
	Nullable []string
	// NonNullable members reject or never carry JSON null where their pointer
	// or Optional type could: the handler refuses a null request value, or
	// the response always fills the pointer.
	NonNullable []string
}

// evaluateDTOSchemaAgreement compares a registered DTO with its component
// schema. Member names, required members, and nullability are derived from
// the DTO's JSON tags and Go types (see jsonFieldRequired and
// evaluateOpenAPIShapeAgreement); exception records only what they cannot
// show.
func evaluateDTOSchemaAgreement(
	violations []openAPIAgreementViolation,
	document openAPIDocument,
	name string,
	dto reflect.Type,
	request bool,
	exception dtoSchemaException,
) []openAPIAgreementViolation {
	target := "schema " + name
	if request {
		target += " (request)"
	}
	schema, exists := document.Components.Schemas[name]
	// A schema that only aliases another describes the same wire shape.
	for alias := 0; exists && schema.Ref != "" && alias < 4; alias++ {
		schema, exists = document.Components.Schemas[strings.TrimPrefix(schema.Ref, "#/components/schemas/")]
	}
	if !exists {
		return appendAgreementViolation(violations, target, "component", "is missing")
	}
	if schema.Type != "object" || schema.AdditionalProperties != false {
		violations = appendAgreementViolation(violations, target, "shape", "is not a closed object")
	}
	for dto.Kind() == reflect.Pointer {
		dto = dto.Elem()
	}
	gotProperties := sortedMapKeys(schema.Properties)
	wantProperties := jsonFieldNames(dto)
	if !reflect.DeepEqual(gotProperties, wantProperties) {
		violations = appendAgreementViolation(violations, target, "fields", fmt.Sprintf("got %v, want DTO fields %v", gotProperties, wantProperties))
	}
	required := func(member jsonMember) bool {
		name := jsonFieldName(member.StructField)
		switch {
		case slices.Contains(exception.Required, name):
			return true
		case slices.Contains(exception.Optional, name):
			return false
		default:
			return jsonFieldRequired(member, request)
		}
	}
	wantRequired := make([]string, 0)
	for _, member := range jsonMembers(dto) {
		if required(member) {
			wantRequired = append(wantRequired, jsonFieldName(member.StructField))
		}
	}
	gotRequired := sortedStrings(schema.Required)
	sort.Strings(wantRequired)
	if !slices.Equal(gotRequired, wantRequired) {
		violations = appendAgreementViolation(violations, target, "required fields", fmt.Sprintf("got %v, DTO requires %v", gotRequired, wantRequired))
	}
	for _, member := range jsonMembers(dto) {
		name := jsonFieldName(member.StructField)
		raw, exists := schema.Properties[name]
		if name == "" || !exists {
			continue
		}
		var shape openAPISchemaShape
		if err := json.Unmarshal(raw, &shape); err != nil {
			violations = appendAgreementViolation(violations, target+"."+name, "decode", err.Error())
			continue
		}
		violations = evaluateOpenAPIShapeAgreement(
			violations,
			document,
			target+"."+name,
			shape,
			member.Type,
			request,
			!required(member),
			slices.Contains(exception.Nullable, name),
			slices.Contains(exception.NonNullable, name),
			nestedAgreementPaths(exception.Nullable, name),
			nestedAgreementPaths(exception.NonNullable, name),
		)
	}
	return violations
}

// jsonMember is a serialized struct field. Promoted marks a field promoted
// from an embedded pointer, which encoding/json omits with a nil embedding.
type jsonMember struct {
	reflect.StructField
	Promoted bool
}

// jsonFieldRequired derives whether a member is always present. A member is
// optional when omitempty can leave it out or a nil embedded pointer drops
// it; a request member is also optional when its pointer or Optional type
// observes absence.
func jsonFieldRequired(member jsonMember, request bool) bool {
	parts := strings.Split(member.Tag.Get("json"), ",")
	if parts[0] == "" || parts[0] == "-" || member.Promoted || slices.Contains(parts[1:], "omitempty") {
		return false
	}
	return !request || !presenceAwareType(member.Type)
}

func presenceAwareType(goType reflect.Type) bool {
	return goType.Kind() == reflect.Pointer || isOptionalType(goType)
}

func isOptionalType(goType reflect.Type) bool {
	return goType.PkgPath() == reflect.TypeOf(route.Optional[string]{}).PkgPath() &&
		strings.HasPrefix(goType.Name(), "Optional[")
}

// evaluateOpenAPIShapeAgreement compares one member's schema with its Go
// type. A pointer or Optional member is nullable when it is an optional
// request member (a JSON null clears or skips it) or a required response
// member (a nil pointer encodes as null); forceNullable and forceNonNullable
// carry the exceptions.
func evaluateOpenAPIShapeAgreement(
	violations []openAPIAgreementViolation,
	document openAPIDocument,
	target string,
	shape openAPISchemaShape,
	goType reflect.Type,
	requestSchema bool,
	fieldOptional bool,
	forceNullable bool,
	forceNonNullable bool,
	nullablePaths []string,
	nonNullablePaths []string,
) []openAPIAgreementViolation {
	if name := agreementTargetField(target); isWireTimestampName(name) {
		violations = evaluateWireTimestampAgreement(violations, target, name, shape, goType, requestSchema)
	}
	nullable := forceNullable || presenceAwareType(goType) && requestSchema == fieldOptional
	for goType.Kind() == reflect.Pointer {
		goType = goType.Elem()
	}
	if isOptionalType(goType) {
		goType = goType.Field(0).Type
	}
	if forceNonNullable {
		nullable = false
	}
	if goType == reflect.TypeOf(model.BrowserActivityEvent{}) {
		return evaluateBrowserEventCodecAgreement(violations, document, target, shape, requestSchema)
	}
	if goType == reflect.TypeOf(model.NativeRecord{}) {
		return evaluateNativeRecordCodecAgreement(violations, document, target, shape, requestSchema)
	}
	if goType == reflect.TypeOf(model.NativeFamilyPolicy{}) {
		return evaluateNativeFamilyCodecAgreement(violations, document, target, shape, requestSchema)
	}
	if goType == reflect.TypeOf(model.SecurityPolicyScope{}) {
		return evaluateSecurityScopeCodecAgreement(violations, document, target, shape, requestSchema)
	}
	if goType == reflect.TypeOf(searchItemResponse{}) {
		return evaluateSearchItemAgreement(violations, document, target, shape, requestSchema)
	}
	unionNullable := false
	if len(shape.OneOf) > 0 {
		if len(shape.OneOf) != 2 {
			return appendAgreementViolation(violations, target, "oneOf", "nullable union must contain exactly one value schema and null")
		}
		var value *openAPISchemaShape
		for index := range shape.OneOf {
			candidate := shape.OneOf[index]
			if openAPITypesEqual(candidate.Type, []string{"null"}) {
				unionNullable = true
				continue
			}
			if value != nil {
				return appendAgreementViolation(violations, target, "oneOf", "nullable union contains multiple value schemas")
			}
			value = &candidate
		}
		if !unionNullable || value == nil {
			return appendAgreementViolation(violations, target, "oneOf", "nullable union is missing its value or null schema")
		}
		shape = *value
		nullable = true
	}
	if shape.Ref != "" {
		const prefix = "#/components/schemas/"
		name := strings.TrimPrefix(shape.Ref, prefix)
		component, exists := document.Components.Schemas[name]
		if name == shape.Ref || !exists {
			return appendAgreementViolation(violations, target, "reference", fmt.Sprintf("unresolved %q", shape.Ref))
		}
		encoded, err := json.Marshal(component)
		if err != nil {
			return appendAgreementViolation(violations, target, "reference", fmt.Sprintf("encode %q: %v", shape.Ref, err))
		}
		if err := json.Unmarshal(encoded, &shape); err != nil {
			return appendAgreementViolation(violations, target, "reference", fmt.Sprintf("decode %q: %v", shape.Ref, err))
		}
	}
	if unionNullable {
		switch value := shape.Type.(type) {
		case string:
			shape.Type = []any{value, "null"}
		case []any:
			shape.Type = append(value, "null")
		}
	}

	if goType == reflect.TypeOf(time.Time{}) {
		wantTimeTypes := []string{"string"}
		if nullable {
			wantTimeTypes = append(wantTimeTypes, "null")
		}
		if !openAPITypesEqual(shape.Type, wantTimeTypes) || shape.Format != "date-time" {
			return appendAgreementViolation(violations, target, "time codec", "must be a date-time string")
		}
		return violations
	}
	wantType := map[reflect.Kind]string{
		reflect.Bool: "boolean", reflect.Int: "integer", reflect.Int8: "integer",
		reflect.Int16: "integer", reflect.Int32: "integer", reflect.Int64: "integer",
		reflect.Uint: "integer", reflect.Uint8: "integer", reflect.Uint16: "integer",
		reflect.Uint32: "integer", reflect.Uint64: "integer", reflect.String: "string",
		reflect.Slice: "array", reflect.Array: "array", reflect.Map: "object",
		reflect.Struct: "object",
	}[goType.Kind()]
	if wantType == "" {
		return appendAgreementViolation(violations, target, "type", fmt.Sprintf("unsupported Go type %s", goType))
	}
	wantTypes := []string{wantType}
	if nullable {
		wantTypes = append(wantTypes, "null")
	}
	if !openAPITypesEqual(shape.Type, wantTypes) {
		return appendAgreementViolation(violations, target, "type", fmt.Sprintf("got %#v, want JSON types %v", shape.Type, wantTypes))
	}
	if goType.Kind() == reflect.Int64 || goType.Kind() == reflect.Uint64 {
		if shape.Format != "int64" {
			violations = appendAgreementViolation(violations, target, "format", fmt.Sprintf("got %q, want int64", shape.Format))
		}
	}
	if goType.Kind() == reflect.Slice || goType.Kind() == reflect.Array {
		if shape.Items == nil {
			return appendAgreementViolation(violations, target, "items", "array item schema is missing")
		}
		return evaluateOpenAPIShapeAgreement(violations, document, target+"[]", *shape.Items, goType.Elem(), requestSchema, false, false, false, nullablePaths, nonNullablePaths)
	}
	if goType.Kind() == reflect.Map {
		var additional openAPISchemaShape
		if goType.Key().Kind() != reflect.String ||
			len(shape.AdditionalProperties) == 0 ||
			json.Unmarshal(shape.AdditionalProperties, &additional) != nil {
			return appendAgreementViolation(violations, target, "additional properties", "map schema does not declare string-keyed values")
		}
		return evaluateOpenAPIShapeAgreement(violations, document, target+"{}", additional, goType.Elem(), requestSchema, false, false, false, nil, nil)
	}
	if goType.Kind() != reflect.Struct {
		return violations
	}

	gotProperties := sortedMapKeys(shape.Properties)
	wantProperties := jsonFieldNames(goType)
	if !reflect.DeepEqual(gotProperties, wantProperties) {
		violations = appendAgreementViolation(violations, target, "fields", fmt.Sprintf("got %v, want Go fields %v", gotProperties, wantProperties))
	}
	gotRequired := sortedStrings(shape.Required)
	wantRequired := requiredJSONFieldNames(goType, requestSchema)
	if !slices.Equal(gotRequired, wantRequired) {
		violations = appendAgreementViolation(violations, target, "required fields", fmt.Sprintf("got %v, Go JSON tags require %v", gotRequired, wantRequired))
	}
	for _, member := range jsonMembers(goType) {
		name := jsonFieldName(member.StructField)
		raw, exists := shape.Properties[name]
		if name == "" || !exists {
			continue
		}
		var property openAPISchemaShape
		if err := json.Unmarshal(raw, &property); err != nil {
			violations = appendAgreementViolation(violations, target+"."+name, "decode", err.Error())
			continue
		}
		violations = evaluateOpenAPIShapeAgreement(
			violations,
			document,
			target+"."+name,
			property,
			member.Type,
			requestSchema,
			!jsonFieldRequired(member, requestSchema),
			slices.Contains(nullablePaths, name),
			slices.Contains(nonNullablePaths, name),
			nestedAgreementPaths(nullablePaths, name),
			nestedAgreementPaths(nonNullablePaths, name),
		)
	}
	return violations
}

func jsonFieldName(field reflect.StructField) string {
	name := strings.Split(field.Tag.Get("json"), ",")[0]
	if name == "-" {
		return ""
	}
	return name
}

func sortedMapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func jsonFieldNames(dto reflect.Type) []string {
	fields := make([]string, 0, dto.NumField())
	for _, member := range jsonMembers(dto) {
		if name := jsonFieldName(member.StructField); name != "" {
			fields = append(fields, name)
		}
	}
	sort.Strings(fields)
	return fields
}

func requiredJSONFieldNames(dto reflect.Type, request bool) []string {
	fields := make([]string, 0, dto.NumField())
	for _, member := range jsonMembers(dto) {
		if jsonFieldRequired(member, request) {
			fields = append(fields, jsonFieldName(member.StructField))
		}
	}
	sort.Strings(fields)
	return fields
}

func nestedAgreementPaths(values []string, field string) []string {
	prefix := field + "."
	var nested []string
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			nested = append(nested, strings.TrimPrefix(value, prefix))
		}
	}
	return nested
}

func openAPITypesEqual(value any, want []string) bool {
	var got []string
	switch types := value.(type) {
	case string:
		got = []string{types}
	case []any:
		for _, candidate := range types {
			value, ok := candidate.(string)
			if !ok {
				return false
			}
			got = append(got, value)
		}
	default:
		return false
	}
	sort.Strings(got)
	want = append([]string(nil), want...)
	sort.Strings(want)
	return reflect.DeepEqual(got, want)
}

// The native family is a custom codec with private typed state. Verify every
// branch against its actual serialized fields instead of reflecting that state.
func evaluateNativeFamilyCodecAgreement(violations []openAPIAgreementViolation, document openAPIDocument, target string, shape openAPISchemaShape, requestSchema bool) []openAPIAgreementViolation {
	families := model.DefaultNativeSecurityPolicy().Families
	if len(shape.OneOf) != len(families) {
		return appendAgreementViolation(violations, target, "oneOf", "must describe all ten native family codec branches")
	}
	for index, family := range families {
		raw, err := json.Marshal(family)
		if err != nil {
			panic(err)
		}
		var members map[string]any
		if err := json.Unmarshal(raw, &members); err != nil {
			panic(err)
		}
		names := make([]string, 0, len(members))
		for name := range members {
			names = append(names, name)
		}
		sort.Strings(names)
		fields := make([]reflect.StructField, 0, len(names))
		for i, name := range names {
			typ := reflect.TypeOf(members[name])
			if _, ok := members[name].([]any); ok {
				typ = reflect.TypeOf([]string{})
			}
			fields = append(fields, reflect.StructField{Name: fmt.Sprintf("Field%d", i), Type: typ, Tag: reflect.StructTag(fmt.Sprintf(`json:"%s"`, name))})
		}
		branch := shape.OneOf[index]
		var id struct {
			Const string `json:"const"`
		}
		_ = json.Unmarshal(branch.Properties["id"], &id)
		if id.Const != family.ID() {
			violations = appendAgreementViolation(violations, target, "family id", fmt.Sprintf("branch %d must identify %s", index, family.ID()))
		}
		if string(branch.AdditionalProperties) != "false" {
			violations = appendAgreementViolation(violations, target, "family shape", "must reject extra fields")
		}
		violations = evaluateOpenAPIShapeAgreement(violations, document, target+"."+family.ID(), branch, reflect.StructOf(fields), requestSchema, false, false, false, nil, nil)
	}
	return violations
}

func jsonMembers(dto reflect.Type) []jsonMember {
	var members []jsonMember
	for i := 0; i < dto.NumField(); i++ {
		field := dto.Field(i)
		embeddedType := field.Type
		if embeddedType.Kind() == reflect.Pointer {
			embeddedType = embeddedType.Elem()
		}
		if field.Anonymous && field.Tag.Get("json") == "" && embeddedType.Kind() == reflect.Struct {
			for _, member := range jsonMembers(embeddedType) {
				member.Promoted = member.Promoted || field.Type.Kind() == reflect.Pointer
				members = append(members, member)
			}
		} else {
			members = append(members, jsonMember{StructField: field})
		}
	}
	return members
}
func evaluateSecurityScopeCodecAgreement(violations []openAPIAgreementViolation, document openAPIDocument, target string, shape openAPISchemaShape, requestSchema bool) []openAPIAgreementViolation {
	scopes := []model.SecurityPolicyScope{{Kind: "admission", AdmissionScopeID: "admission"}, {Kind: "attempt", AttemptID: model.NewExamAttemptID()}}
	if len(shape.OneOf) != len(scopes) {
		return appendAgreementViolation(violations, target, "scope union", "must contain exact admission and attempt variants")
	}
	for i, scope := range scopes {
		raw, _ := json.Marshal(scope)
		var members map[string]string
		if err := json.Unmarshal(raw, &members); err != nil {
			panic(err)
		}
		names := make([]string, 0, len(members))
		for name := range members {
			names = append(names, name)
		}
		sort.Strings(names)
		fields := make([]reflect.StructField, 0, len(names))
		for j, name := range names {
			fields = append(fields, reflect.StructField{Name: fmt.Sprintf("Field%d", j), Type: reflect.TypeOf(""), Tag: reflect.StructTag(fmt.Sprintf(`json:"%s"`, name))})
		}
		branch := shape.OneOf[i]
		var kind struct {
			Const string `json:"const"`
		}
		_ = json.Unmarshal(branch.Properties["kind"], &kind)
		if kind.Const != scope.Kind || string(branch.AdditionalProperties) != "false" {
			violations = appendAgreementViolation(violations, target, "scope variant", "must be closed and identify the exact kind")
		}
		violations = evaluateOpenAPIShapeAgreement(violations, document, target+"."+scope.Kind, branch, reflect.StructOf(fields), requestSchema, false, false, false, nil, nil)
	}
	return violations
}

// evaluateSearchItemAgreement holds the closed search item union to its three
// variant DTOs: each branch is a closed object whose type member is the
// constant its discriminator mapping names.
func evaluateSearchItemAgreement(violations []openAPIAgreementViolation, document openAPIDocument, target string, shape openAPISchemaShape, requestSchema bool) []openAPIAgreementViolation {
	if shape.Ref != "" {
		component, ok := document.Components.Schemas[strings.TrimPrefix(shape.Ref, "#/components/schemas/")]
		if !ok {
			return appendAgreementViolation(violations, target, "reference", "missing search item schema")
		}
		raw, err := json.Marshal(component)
		if err != nil {
			panic(err)
		}
		if err := json.Unmarshal(raw, &shape); err != nil {
			panic(err)
		}
	}
	variants := []struct {
		kind string
		dto  reflect.Type
	}{
		{"exam", reflect.TypeOf(searchExamItemResponse{})},
		{"class", reflect.TypeOf(searchClassItemResponse{})},
		{"user", reflect.TypeOf(searchUserItemResponse{})},
	}
	if len(shape.OneOf) != len(variants) {
		return appendAgreementViolation(violations, target, "oneOf", "must describe the three closed search item variants")
	}
	for i, variant := range variants {
		branch := shape.OneOf[i]
		if branch.Ref != "" {
			component, ok := document.Components.Schemas[strings.TrimPrefix(branch.Ref, "#/components/schemas/")]
			if !ok {
				violations = appendAgreementViolation(violations, target+"."+variant.kind, "reference", "missing search item variant")
				continue
			}
			raw, err := json.Marshal(component)
			if err != nil {
				panic(err)
			}
			branch = openAPISchemaShape{}
			if err := json.Unmarshal(raw, &branch); err != nil {
				panic(err)
			}
		}
		var kind struct {
			Const string `json:"const"`
		}
		_ = json.Unmarshal(branch.Properties["type"], &kind)
		if kind.Const != variant.kind || string(branch.AdditionalProperties) != "false" {
			violations = appendAgreementViolation(violations, target+"."+variant.kind, "variant", "must be closed and identify the exact type")
		}
		violations = evaluateOpenAPIShapeAgreement(violations, document, target+"."+variant.kind, branch, variant.dto, requestSchema, false, false, false, nil, nil)
	}
	return violations
}

func evaluateNativeRecordCodecAgreement(violations []openAPIAgreementViolation, document openAPIDocument, target string, shape openAPISchemaShape, requestSchema bool) []openAPIAgreementViolation {
	if shape.Ref != "" {
		component, ok := document.Components.Schemas[strings.TrimPrefix(shape.Ref, "#/components/schemas/")]
		if !ok {
			return appendAgreementViolation(violations, target, "reference", "missing native record schema")
		}
		raw, err := json.Marshal(component)
		if err != nil {
			panic(err)
		}
		if err := json.Unmarshal(raw, &shape); err != nil {
			panic(err)
		}
	}
	variants := []reflect.Type{reflect.TypeOf(model.NativeOccurrence{}), reflect.TypeOf(model.NativeCoverageTransition{}), reflect.TypeOf(model.NativeSourceReset{}), reflect.TypeOf(model.NativeSourceGap{})}
	if len(shape.OneOf) != len(variants) {
		return appendAgreementViolation(violations, target, "oneOf", "must describe the four closed native record variants")
	}
	for i, variant := range variants {
		violations = evaluateOpenAPIShapeAgreement(violations, document, target+"."+variant.Name(), shape.OneOf[i], variant, requestSchema, false, false, false, nil, nil)
	}
	return violations
}

// The Browser event codec is a closed discriminated union, despite its compact
// in-memory representation. These independent wire shapes cover its five cases.
type browserLifecycleAgreement struct {
	Sequence         int64     `json:"sequence"`
	Kind             string    `json:"kind"`
	PolicyRevisionID string    `json:"policy_revision_id"`
	ClientOccurredAt time.Time `json:"client_occurred_at"`
}
type browserNavigationAgreement struct {
	browserLifecycleAgreement
	Location      model.BrowserLocation `json:"location"`
	MatchedRuleID string                `json:"matched_rule_id"`
}
type browserRedirectAgreement struct {
	browserNavigationAgreement
	RedirectFromSequence int64 `json:"redirect_from_sequence"`
}
type browserBlockedAgreement struct {
	browserLifecycleAgreement
	Location             model.BrowserLocation `json:"location"`
	MatchedRuleID        string                `json:"matched_rule_id,omitempty"`
	BlockReason          string                `json:"block_reason"`
	RedirectFromSequence int64                 `json:"redirect_from_sequence,omitempty"`
}

func evaluateBrowserEventCodecAgreement(violations []openAPIAgreementViolation, document openAPIDocument, target string, shape openAPISchemaShape, request bool) []openAPIAgreementViolation {
	if shape.Ref != "" {
		component, ok := document.Components.Schemas[strings.TrimPrefix(shape.Ref, "#/components/schemas/")]
		if !ok {
			return appendAgreementViolation(violations, target, "reference", "missing Browser record schema")
		}
		raw, err := json.Marshal(component)
		if err != nil {
			panic(err)
		}
		if err := json.Unmarshal(raw, &shape); err != nil {
			panic(err)
		}
	}
	variants := []reflect.Type{reflect.TypeOf(browserLifecycleAgreement{}), reflect.TypeOf(browserLifecycleAgreement{}), reflect.TypeOf(browserNavigationAgreement{}), reflect.TypeOf(browserRedirectAgreement{}), reflect.TypeOf(browserBlockedAgreement{})}
	if len(shape.OneOf) != len(variants) {
		return appendAgreementViolation(violations, target, "oneOf", "must describe five closed Browser record variants")
	}
	for i, variant := range variants {
		violations = evaluateOpenAPIShapeAgreement(violations, document, fmt.Sprintf("%s.variant%d", target, i), shape.OneOf[i], variant, request, false, false, false, nil, nil)
	}
	return violations
}

// legacyWireTimestampNames are the retired millisecond-era member names.
var legacyWireTimestampNames = map[string]string{
	"create_at": "created_at", "update_at": "updated_at", "delete_at": "archived_at",
}

func agreementTargetField(target string) string {
	name := target[strings.LastIndex(target, ".")+1:]
	return strings.TrimRight(name, "[]{}")
}

// isWireTimestampName reports whether a JSON member names an instant. Such
// members use the one wire timestamp dialect: an RFC 3339 date-time string
// that is omitted, never null, when a response has no value.
func isWireTimestampName(name string) bool {
	if _, legacy := legacyWireTimestampNames[name]; legacy {
		return true
	}
	if name == "awaiting_deadline" { // a count of records awaiting their deadline
		return false
	}
	return strings.HasSuffix(name, "_at") || strings.HasSuffix(name, "deadline") ||
		name == "message_date" || name == "server_time" || name == "database_time"
}

func evaluateWireTimestampAgreement(
	violations []openAPIAgreementViolation,
	target string,
	name string,
	shape openAPISchemaShape,
	goType reflect.Type,
	requestSchema bool,
) []openAPIAgreementViolation {
	if replacement, legacy := legacyWireTimestampNames[name]; legacy {
		violations = appendAgreementViolation(violations, target, "timestamp name", "use "+replacement)
	}
	for goType.Kind() == reflect.Pointer {
		goType = goType.Elem()
	}
	if goType.PkgPath() == reflect.TypeOf(route.Optional[string]{}).PkgPath() && strings.HasPrefix(goType.Name(), "Optional[") {
		goType = goType.Field(0).Type
	}
	if goType != reflect.TypeOf(time.Time{}) && goType.Kind() != reflect.String {
		violations = appendAgreementViolation(violations, target, "timestamp dialect", fmt.Sprintf("Go type %s must carry an RFC 3339 string", goType))
	}
	value := shape
	nullable := openAPITypesEqual(shape.Type, []string{"string", "null"})
	for _, candidate := range shape.OneOf {
		if openAPITypesEqual(candidate.Type, []string{"null"}) {
			nullable = true
			continue
		}
		value = candidate
	}
	if value.Ref == "" && value.Format != "date-time" {
		violations = appendAgreementViolation(violations, target, "timestamp dialect", "must be a date-time string")
	}
	// Model-owned canonical security documents (time.Time codecs such as
	// DeliveryClosure) keep their frozen closed-record shape with explicit
	// nulls; the omission rule binds transport-owned string members.
	if nullable && !requestSchema && goType.Kind() == reflect.String {
		violations = appendAgreementViolation(violations, target, "timestamp absence", "absent response timestamps are omitted, never null")
	}
	return violations
}
