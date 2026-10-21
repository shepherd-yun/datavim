package schemadiff

import (
	"fmt"
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
)

// Describe is c on one line, as the diff tab lists it: a sign (+ added,
// - removed, ~ changed), what it is about, and the target's value → the
// source's.
func Describe(c db.SchemaChange) string {
	switch c.Kind {
	case db.AddColumn:
		return "+ column " + c.New.Name + "  " + columnText(*c.New)
	case db.DropColumn:
		return "- column " + c.Old.Name + "  " + columnText(*c.Old)
	case db.RenameColumn:
		return "~ column " + c.Old.Name + " → " + c.New.Name + "  (rename)"
	case db.AlterColumn:
		return "~ column " + c.New.Name + "  " + attrText(*c.Old, c.Attrs) + " → " + attrText(*c.New, c.Attrs)
	case db.AddIndex:
		return "+ " + indexText(*c.NewIndex)
	case db.DropIndex:
		return "- " + indexText(*c.OldIndex)
	case db.AlterTableComment:
		return "~ comment " + quoted(c.OldValue) + " → " + quoted(c.NewValue)
	case db.AlterTableOption:
		return "~ " + c.Option + " " + c.OldValue + " → " + c.NewValue
	case db.ForeignKeyDiff:
		switch {
		case c.OldFK == nil:
			return "+ foreign key " + c.NewFK.Name + " " + refText(*c.NewFK)
		case c.NewFK == nil:
			return "- foreign key " + c.OldFK.Name + " " + refText(*c.OldFK)
		}
		return "~ foreign key " + c.NewFK.Name + " " + refText(*c.OldFK) + " → " + refText(*c.NewFK)
	}
	return ""
}

// DescribeObject is the line of a whole table.
func DescribeObject(o ObjectDiff) string {
	switch o.Kind {
	case CreateTable:
		n := len(o.Source.Columns)
		noun := "columns"
		if n == 1 {
			noun = "column"
		}
		return fmt.Sprintf("+ %s  (new table, %d %s)", o.Name, n, noun)
	case DropTable:
		return "- " + o.Name
	}
	return "~ " + o.Name
}

func columnText(c db.Column) string {
	parts := []string{c.Type, nullText(c.Nullable)}
	if c.Default != nil {
		parts = append(parts, "DEFAULT "+*c.Default)
	}
	if c.OnUpdate != "" {
		parts = append(parts, "ON UPDATE "+c.OnUpdate)
	}
	if c.AutoIncrement {
		parts = append(parts, "auto increment")
	}
	if c.Generated != "" {
		parts = append(parts, "AS ("+c.Generated+")")
	}
	return strings.Join(parts, " ")
}

// attrText shows the attributes of c that a flags.
func attrText(c db.Column, a db.ColumnAttr) string {
	var parts []string
	if a&db.AttrType != 0 {
		parts = append(parts, c.Type)
	}
	if a&db.AttrNullable != 0 {
		parts = append(parts, nullText(c.Nullable))
	}
	if a&db.AttrDefault != 0 {
		if c.Default == nil {
			parts = append(parts, "no default")
		} else {
			parts = append(parts, "DEFAULT "+*c.Default)
		}
		if c.OnUpdate != "" {
			parts = append(parts, "ON UPDATE "+c.OnUpdate)
		}
	}
	if a&db.AttrCollation != 0 {
		if c.Collation == "" {
			parts = append(parts, "default collation")
		} else {
			parts = append(parts, "COLLATE "+c.Collation)
		}
	}
	if a&db.AttrAutoIncrement != 0 {
		if c.AutoIncrement {
			parts = append(parts, "auto increment")
		} else {
			parts = append(parts, "no auto increment")
		}
	}
	if a&db.AttrGenerated != 0 {
		if c.Generated == "" {
			parts = append(parts, "not generated")
		} else {
			parts = append(parts, "AS ("+c.Generated+")")
		}
	}
	if a&db.AttrComment != 0 {
		parts = append(parts, "comment "+quoted(c.Comment))
	}
	return strings.Join(parts, " ")
}

func nullText(nullable bool) string {
	if nullable {
		return "NULL"
	}
	return "NOT NULL"
}

func indexText(ix db.Index) string {
	cols := "(" + strings.Join(ix.Columns, ", ") + ")"
	if ix.Primary {
		return "primary key " + cols
	}
	s := "index " + ix.Name + " " + cols
	if ix.Unique {
		s += " unique"
	}
	if ix.Method != "" && ix.Method != "btree" {
		s += " " + ix.Method
	}
	if ix.Where != "" {
		s += " WHERE " + ix.Where
	}
	return s
}

func refText(fk db.ForeignKey) string {
	return "(" + strings.Join(fk.Columns, ", ") + ") REFERENCES " + fk.RefTable.Name + " (" + strings.Join(fk.RefColumns, ", ") + ")"
}

// quoted shows a comment; for people, not SQL.
func quoted(s string) string { return "'" + s + "'" }
