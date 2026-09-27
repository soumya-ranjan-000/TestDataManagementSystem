// Package dictionary is the versioned vocabulary from "Rule schema and
// dictionary": which fields, operators, and enum values a validity rule may
// legally reference. It enforces one check — a rule is legal only if its
// field exists, its operator suits that field's type, and its value is
// valid for that field — before any rule reaches the live PNR evaluator.
package dictionary

// DataType is the type system for dictionary fields: enum, datetime,
// integer, string, or boolean. The type decides which operators are legal
// on the field.
type DataType string

const (
	TypeEnum     DataType = "enum"
	TypeDatetime DataType = "datetime"
	TypeInteger  DataType = "integer"
	TypeString   DataType = "string"
	TypeBoolean  DataType = "boolean"
)

// Field is one PNR attribute a rule may reference: its canonical path, its
// data type, and — for enum fields — its allowed value set.
type Field struct {
	Path          string
	Type          DataType
	AllowedValues []string // only meaningful when Type == TypeEnum
}

// Operator is one test, tagged with the data types it works on.
type Operator struct {
	Name      string
	AppliesTo []DataType
}

// Dictionary is the versioned vocabulary: fields plus operators. It's
// seeded narrow (only what today's rules reference) and grows rule by
// rule, never catalogued upfront — see "Three practical constraints".
type Dictionary struct {
	Version   string
	Fields    map[string]Field
	Operators map[string]Operator
}

func (d *Dictionary) Field(path string) (Field, bool) {
	f, ok := d.Fields[path]
	return f, ok
}

func (d *Dictionary) Operator(name string) (Operator, bool) {
	o, ok := d.Operators[name]
	return o, ok
}

// OperatorAppliesTo reports whether operator opName is legal on data type t.
func (d *Dictionary) OperatorAppliesTo(opName string, t DataType) bool {
	op, ok := d.Operators[opName]
	if !ok {
		return false
	}
	for _, dt := range op.AppliesTo {
		if dt == t {
			return true
		}
	}
	return false
}
