package dictionary

// Seed returns the v1.0.0 dictionary: only the fields and operators today's
// two rule templates (booking_status, checkin_window) actually reference.
// A PNR has hundreds of attributes; this deliberately catalogues none of
// them upfront, per "Seeded narrow".
func Seed() *Dictionary {
	return &Dictionary{
		Version: "1.0.0",
		Fields: map[string]Field{
			"booking.status": {
				Path:          "booking.status",
				Type:          TypeEnum,
				AllowedValues: []string{"CONFIRMED", "TICKETED", "CANCELLED"},
			},
			"segment.travel_date": {
				Path: "segment.travel_date",
				Type: TypeDatetime,
			},
		},
		Operators: map[string]Operator{
			"equals": {Name: "equals", AppliesTo: []DataType{TypeEnum, TypeString}},
			"in":     {Name: "in", AppliesTo: []DataType{TypeEnum, TypeString}},
			"before": {Name: "before", AppliesTo: []DataType{TypeDatetime}},
			"after":  {Name: "after", AppliesTo: []DataType{TypeDatetime}},
			"within": {Name: "within", AppliesTo: []DataType{TypeDatetime}},
		},
	}
}
