// Package directives defines and provides helpers for Argo-specific GraphQL directives
// used to control serialization, deserialization, and other behaviors.
package directives

import (
	"fmt"
	"strconv"

	"github.com/vektah/gqlparser/v2/ast"
)

const (
	ArgoDeduplicateDirectiveName = "ArgoDeduplicate"
	ArgoCodecDirectiveName       = "ArgoCodec"
)

// ArgoDeduplicateDirective represents the @ArgoDeduplicate directive.
// Marks a type for de-duplication. Works best with large values which re-appear often.
var ArgoDeduplicateDirective = &ast.DirectiveDefinition{
	Name:        ArgoDeduplicateDirectiveName,
	Description: "Deduplicate values of this type. Adding or removing this directive is typically a breaking change.",
	Locations: []ast.DirectiveLocation{
		ast.LocationScalar,
		ast.LocationEnum,
		// ast.LocationObject,
		// ast.LocationInterface,
		// ast.LocationFieldDefinition,
	},
	Arguments: ast.ArgumentDefinitionList{
		{
			Name: "deduplicate",
			Type: ast.NonNullNamedType("Boolean", nil),
			DefaultValue: &ast.Value{
				Raw:          "true",
				Kind:         ast.BooleanValue,
				ExpectedType: ast.NamedType("Boolean", nil),
			},
			Description: "Should values of this type be deduplicated?",
		},
	},
	IsRepeatable: false,
}

// ArgoCodec represents the available Argo codecs values from the @ArgoCodec directive's 'codec' argument.
// This is distinct from Argo Wire TypeKeys.
type ArgoCodecValue string

const (
	ArgoCodecValueString  ArgoCodecValue = "String"
	ArgoCodecValueInt     ArgoCodecValue = "Int"
	ArgoCodecValueFloat   ArgoCodecValue = "Float"
	ArgoCodecValueBoolean ArgoCodecValue = "Boolean"
	ArgoCodecValueBYTES   ArgoCodecValue = "BYTES"
	ArgoCodecValueFIXED   ArgoCodecValue = "FIXED"
	ArgoCodecValueDESC    ArgoCodecValue = "DESC"
	ArgoCodecValuePATH    ArgoCodecValue = "PATH"
)

// ArgoCodecType is the GraphQL enum definition for the 'codec' argument of @ArgoCodec.
var ArgoCodecType = &ast.Definition{
	Kind:        ast.Enum,
	Name:        "ArgoCodecType",
	Description: "Specifies how to serialize and deserialize this scalar or the underlying type of a block.",
	EnumValues: ast.EnumValueList{
		{Name: string(ArgoCodecValueString), Description: "Serialize and deserialize a scalar as a GraphQL String (UTF-8)."},
		{Name: string(ArgoCodecValueInt), Description: "Serialize and deserialize a scalar as a GraphQL Int (implies Argo Varint)."},
		{Name: string(ArgoCodecValueFloat), Description: "Serialize and deserialize a scalar as a GraphQL Float (IEEE 754 double-precision floating-point)."},
		{Name: string(ArgoCodecValueBoolean), Description: "Serialize and deserialize a scalar as a GraphQL Boolean."},
		{Name: string(ArgoCodecValueBYTES), Description: "Serialize and deserialize a scalar as Argo BYTES: a variable-length length-prefixed byte array."},
		{Name: string(ArgoCodecValueFIXED), Description: "Serialize and deserialize a scalar as Argo FIXED: a fixed-length byte array. Requires fixedLength arg."},
		{Name: string(ArgoCodecValueDESC), Description: "Serialize and deserialize a scalar as Argo DESC: a flexible self-describing binary format."},
		{Name: string(ArgoCodecValuePATH), Description: "Serialize and deserialize a scalar as Argo PATH: a structured path, often used for hierarchical data."},
	},
}

// ArgoCodecDirective represents the @ArgoCodec directive.
var ArgoCodecDirective = &ast.DirectiveDefinition{
	Name:        ArgoCodecDirectiveName,
	Description: "Specifies encoding for a type, potentially as a block. Adding, changing, or removing this directive is typically a breaking change.",
	Locations: []ast.DirectiveLocation{
		ast.LocationScalar,
		ast.LocationEnum,
		// ast.LocationObject,
		// ast.LocationInterface,
		// ast.LocationFieldDefinition,
	},
	Arguments: ast.ArgumentDefinitionList{
		{
			Name:        "codec",
			Type:        ast.NamedType("ArgoCodecType", nil),
			Description: "If specified, defines the underlying Argo wire type for a scalar, enum, or the content of a block.",
		},
		{
			Name:        "key",
			Type:        ast.NamedType("String", nil),
			Description: "If specified, encodes this type as an Argo Block with the given string as the block key. Often used with @ArgoDeduplicate.",
		},
		{
			Name:        "fixedLength",
			Type:        ast.NamedType("Int", nil),
			Description: "For the FIXED codec only: the length of the encoded value in bytes. Required if codec is FIXED.",
		},
	},
	IsRepeatable: false,
}

// getDirective is a helper function to find a directive by name from a list of directives.
// It returns the first matching directive or nil if not found.
func getDirective(directives ast.DirectiveList, name string) *ast.Directive {
	for _, d := range directives {
		if d.Name == name {
			return d
		}
	}
	return nil
}

// ArgoCodecArgs holds the parsed arguments from an @ArgoCodec directive.
// Pointers are used for optional arguments.
type ArgoCodecArgs struct {
	Codec       *ArgoCodecValue
	FixedLength *int
	Key         *string
}

// GetArgoCodecArgs parses an @ArgoCodec directive from a list of directives.
// It returns the populated ArgoCodecArgs struct and an error if parsing fails
// or if arguments are inconsistent (e.g., fixedLength without FIXED codec).
// If the directive is not present, it returns an empty ArgoCodecArgs and no error.
func GetArgoCodecArgs(directives ast.DirectiveList) (ArgoCodecArgs, error) {
	args := ArgoCodecArgs{}
	directive := getDirective(directives, ArgoCodecDirectiveName)
	if directive == nil {
		return args, nil
	}

	codecArgVal := directive.Arguments.ForName("codec")
	if codecArgVal != nil && codecArgVal.Value != nil && codecArgVal.Value.Kind != ast.NullValue {
		if codecArgVal.Value.Kind != ast.EnumValue && codecArgVal.Value.Kind != ast.StringValue {
			return args, fmt.Errorf("codec argument on @%s must be an ArgoCodecType enum value, got %v", ArgoCodecDirectiveName, codecArgVal.Value.Kind)
		}
		rawCodecStr := codecArgVal.Value.Raw
		codec := ArgoCodecValue(rawCodecStr)
		switch codec {
		case ArgoCodecValueString, ArgoCodecValueInt, ArgoCodecValueFloat, ArgoCodecValueBoolean, ArgoCodecValueBYTES, ArgoCodecValueFIXED, ArgoCodecValueDESC, ArgoCodecValuePATH:
			args.Codec = &codec
		default:
			return args, fmt.Errorf("invalid value for codec argument on @%s: '%s'", ArgoCodecDirectiveName, rawCodecStr)
		}
	}

	fixedLengthArgVal := directive.Arguments.ForName("fixedLength")
	if fixedLengthArgVal != nil && fixedLengthArgVal.Value != nil && fixedLengthArgVal.Value.Kind != ast.NullValue {
		if args.Codec == nil || *args.Codec != ArgoCodecValueFIXED {
			return args, fmt.Errorf("fixedLength argument on @%s is only allowed if codec is FIXED", ArgoCodecDirectiveName)
		}
		if fixedLengthArgVal.Value.Kind != ast.IntValue {
			return args, fmt.Errorf("fixedLength argument on @%s must be an Int, got %v", ArgoCodecDirectiveName, fixedLengthArgVal.Value.Kind)
		}
		l, err := strconv.Atoi(fixedLengthArgVal.Value.Raw)
		if err != nil {
			return args, fmt.Errorf("fixedLength argument on @%s is not a valid integer: %s", ArgoCodecDirectiveName, fixedLengthArgVal.Value.Raw)
		}
		args.FixedLength = &l
	} else if args.Codec != nil && *args.Codec == ArgoCodecValueFIXED {
		return args, fmt.Errorf("fixedLength argument is required on @%s when codec is FIXED", ArgoCodecDirectiveName)
	}

	keyArgVal := directive.Arguments.ForName("key")
	if keyArgVal != nil && keyArgVal.Value != nil && keyArgVal.Value.Kind != ast.NullValue {
		if keyArgVal.Value.Kind != ast.StringValue {
			return args, fmt.Errorf("key argument on @%s must be a String, got %v", ArgoCodecDirectiveName, keyArgVal.Value.Kind)
		}
		keyStr := keyArgVal.Value.Raw
		args.Key = &keyStr
	}

	return args, nil
}

// GetArgoDeduplicateValue parses an @ArgoDeduplicate directive from a list of directives.
// It returns the boolean value of the 'deduplicate' argument, a flag indicating if the
// directive was present, and an error if parsing fails.
// If the directive is not present, it returns (false, false, nil).
// If the directive is present but the argument is missing, it defaults to (true, true, nil).
func GetArgoDeduplicateValue(directives ast.DirectiveList) (value bool, isPresent bool, err error) {
	directive := getDirective(directives, ArgoDeduplicateDirectiveName)
	if directive == nil {
		return false, false, nil
	}

	deduplicateArg := directive.Arguments.ForName("deduplicate")
	if deduplicateArg == nil || deduplicateArg.Value == nil || deduplicateArg.Value.Kind == ast.NullValue {
		return true, true, nil
	}

	if deduplicateArg.Value.Kind != ast.BooleanValue {
		return false, true, fmt.Errorf("deduplicate argument on @%s is not a boolean, got %v", ArgoDeduplicateDirectiveName, deduplicateArg.Value.Kind)
	}

	boolVal, parseErr := strconv.ParseBool(deduplicateArg.Value.Raw)
	if parseErr != nil {
		return false, true, fmt.Errorf("could not parse boolean value for deduplicate argument on @%s directive: %w", ArgoDeduplicateDirectiveName, parseErr)
	}
	return boolVal, true, nil
}
