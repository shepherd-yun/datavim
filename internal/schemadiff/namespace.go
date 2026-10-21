package schemadiff

import (
	"sort"

	"github.com/shepherd-yun/datavim/internal/db"
)

// ObjectKind says how a table differs between the two sides.
type ObjectKind int

const (
	CreateTable ObjectKind = iota // only the source has the table
	DropTable                     // only the target has it
	AlterTable                    // both have it, with Changes
)

// ObjectDiff is one table that differs between two namespaces.
type ObjectDiff struct {
	Kind    ObjectKind
	Name    string
	Source  *db.TableSchema   // nil for DropTable
	Target  *db.TableSchema   // nil for CreateTable
	Changes []db.SchemaChange // AlterTable only
}

// DiffNamespace pairs the tables of two namespaces by name and lists those
// that differ, sorted by name; identical counts the tables both sides
// have alike. The objects point into target and source.
func DiffNamespace(target, source []db.TableSchema) (objs []ObjectDiff, identical int) {
	byName := map[string]*db.TableSchema{}
	for i := range target {
		byName[target[i].Ref.Name] = &target[i]
	}
	inSource := map[string]bool{}
	for i := range source {
		s := &source[i]
		inSource[s.Ref.Name] = true
		t, ok := byName[s.Ref.Name]
		if !ok {
			objs = append(objs, ObjectDiff{Kind: CreateTable, Name: s.Ref.Name, Source: s})
			continue
		}
		changes := DiffTable(*t, *s, nil)
		if len(changes) == 0 {
			identical++
			continue
		}
		objs = append(objs, ObjectDiff{Kind: AlterTable, Name: s.Ref.Name, Source: s, Target: t, Changes: changes})
	}
	for i := range target {
		if !inSource[target[i].Ref.Name] {
			objs = append(objs, ObjectDiff{Kind: DropTable, Name: target[i].Ref.Name, Target: &target[i]})
		}
	}
	sort.SliceStable(objs, func(i, j int) bool { return objs[i].Name < objs[j].Name })
	return objs, identical
}
