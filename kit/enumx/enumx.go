// Package enumx is the catalog of the enums of a project: every allowed value of a
// status, a provider or a type with its description, for clients and for validation.
// The catalog is generated from the go-enum markers of the domain package, as in taply.
package enumx

import "slices"

// Value is one allowed value.
type Value struct {
	Value       string `json:"value"`
	Description string `json:"description"`
}

// Enum is one enum of an entity, such as the status of an order.
type Enum struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Values      []Value `json:"values"`
}

// Entity groups the enums of one domain entity.
type Entity struct {
	Entity      string `json:"entity"`
	Description string `json:"description"`
	Enums       []Enum `json:"enums"`
}

// Catalog is every enum of the project, ordered as the markers ask.
type Catalog []Entity

// Find returns an enum by entity and name.
func (c Catalog) Find(entity, name string) (Enum, bool) {
	for _, e := range c {
		if e.Entity != entity {
			continue
		}
		for _, en := range e.Enums {
			if en.Name == name {
				return en, true
			}
		}
	}
	return Enum{}, false
}

// Valid reports whether a value is allowed for an enum, for validating input.
func (c Catalog) Valid(entity, name, value string) bool {
	en, ok := c.Find(entity, name)
	return ok && slices.ContainsFunc(en.Values, func(v Value) bool { return v.Value == value })
}
