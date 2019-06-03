package sql

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cast"
	"gopkg.in/src-d/go-errors.v1"
	"gopkg.in/src-d/go-vitess.v1/sqltypes"
	"gopkg.in/src-d/go-vitess.v1/vt/proto/query"
)

var (
	// ErrTypeNotSupported is thrown when a specific type is not supported
	ErrTypeNotSupported = errors.NewKind("Type not supported: %s")

	// ErrUnexpectedType is thrown when a received type is not the expected
	ErrUnexpectedType = errors.NewKind("value at %d has unexpected type: %s")

	// ErrConvertingToTime is thrown when a value cannot be converted to a Time
	ErrConvertingToTime = errors.NewKind("value %q can't be converted to time.Time")

	// ErrValueNotNil is thrown when a value that was expected to be nil, is not
	ErrValueNotNil = errors.NewKind("value not nil: %#v")

	// ErrNotTuple is retuned when the value is not a tuple.
	ErrNotTuple = errors.NewKind("value of type %T is not a tuple")

	// ErrInvalidColumnNumber is returned when a tuple has an invalid number of
	// arguments.
	ErrInvalidColumnNumber = errors.NewKind("tuple should contain %d column(s), but has %d")

	// ErrNotArray is returned when the value is not an array.
	ErrNotArray = errors.NewKind("value of type %T is not an array")

	// ErrConvertToSQL is returned when Convert failed.
	// It makes an error less verbose comparingto what spf13/cast returns.
	ErrConvertToSQL = errors.NewKind("incompatible conversion to SQL type: %s")
)

// Schema is the definition of a table.
type Schema []*Column

// CheckRow checks the row conforms to the schema.
func (s Schema) CheckRow(row Row) error {
	expected := len(s)
	got := len(row)
	if expected != got {
		return ErrUnexpectedRowLength.New(expected, got)
	}

	for idx, f := range s {
		v := row[idx]
		if f.Check(v) {
			continue
		}

		typ := reflect.TypeOf(v).String()
		return ErrUnexpectedType.New(idx, typ)
	}

	return nil
}

// Contains returns whether the schema contains a column with the given name.
func (s Schema) Contains(column string, source string) bool {
	return s.IndexOf(column, source) >= 0
}

// IndexOf returns the index of the given column in the schema or -1 if it's
// not present.
func (s Schema) IndexOf(column, source string) int {
	column = strings.ToLower(column)
	source = strings.ToLower(source)
	for i, col := range s {
		if strings.ToLower(col.Name) == column && strings.ToLower(col.Source) == source {
			return i
		}
	}
	return -1
}

// Equals checks whether the given schema is equal to this one.
func (s Schema) Equals(s2 Schema) bool {
	if len(s) != len(s2) {
		return false
	}

	for i := range s {
		if !s[i].Equals(s2[i]) {
			return false
		}
	}

	return true
}

// Column is the definition of a table column.
// As SQL:2016 puts it:
//   A column is a named component of a table. It has a data type, a default,
//   and a nullability characteristic.
type Column struct {
	// Name is the name of the column.
	Name string
	// Type is the data type of the column.
	Type Type
	// Default contains the default value of the column or nil if it is NULL.
	Default interface{}
	// Nullable is true if the column can contain NULL values, or false
	// otherwise.
	Nullable bool
	// Source is the name of the table this column came from.
	Source string
}

// Check ensures the value is correct for this column.
func (c *Column) Check(v interface{}) bool {
	if v == nil {
		return c.Nullable
	}

	_, err := c.Type.Convert(v)
	return err == nil
}

// Equals checks whether two columns are equal.
func (c *Column) Equals(c2 *Column) bool {
	return c.Name == c2.Name &&
		c.Source == c2.Source &&
		c.Nullable == c2.Nullable &&
		reflect.DeepEqual(c.Default, c2.Default) &&
		reflect.DeepEqual(c.Type, c2.Type)
}

// Type represent a SQL type.
type Type interface {
	// Type returns the query.Type for the given Type.
	Type() query.Type
	// Covert a value of a compatible type to a most accurate type.
	Convert(interface{}) (interface{}, error)
	// Compare returns an integer comparing two values.
	// The result will be 0 if a==b, -1 if a < b, and +1 if a > b.
	Compare(interface{}, interface{}) (int, error)
	// SQL returns the sqltypes.Value for the given value.
	SQL(interface{}) (sqltypes.Value, error)
	fmt.Stringer
}

var maxTime = time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC)

// ValidateTime receives a time and returns either that time or nil if it's
// not a valid time.
func ValidateTime(t time.Time) interface{} {
	if t.After(maxTime) {
		return nil
	}
	return t
}

var (
	// Null represents the null type.
	Null nullT

	// Numeric types

	// Int8 is an integer of 8 bits
	Int8 = numberT{t: sqltypes.Int8}
	// Uint8 is an unsigned integer of 8 bits
	Uint8 = numberT{t: sqltypes.Uint8}
	// Int16 is an integer of 16 bits
	Int16 = numberT{t: sqltypes.Int16}
	// Uint16 is an unsigned integer of 16 bits
	Uint16 = numberT{t: sqltypes.Uint16}
	// Int32 is an integer of 32 bits.
	Int32 = numberT{t: sqltypes.Int32}
	// Int64 is an integer of 64 bytes.
	Int64 = numberT{t: sqltypes.Int64}
	// Uint32 is an unsigned integer of 32 bytes.
	Uint32 = numberT{t: sqltypes.Uint32}
	// Uint64 is an unsigned integer of 64 bytes.
	Uint64 = numberT{t: sqltypes.Uint64}
	// Float32 is a floating point number of 32 bytes.
	Float32 = numberT{t: sqltypes.Float32}
	// Float64 is a floating point number of 64 bytes.
	Float64 = numberT{t: sqltypes.Float64}

	// Timestamp is an UNIX timestamp.
	Timestamp timestampT
	// Date is a date with day, month and year.
	Date dateT
	// Text is a string type.
	Text textT
	// Boolean is a boolean type.
	Boolean booleanT
	// JSON is a type that holds any valid JSON object.
	JSON jsonT
	// Blob is a type that holds a chunk of binary data.
	Blob blobT
)

// Tuple returns a new tuple type with the given element types.
func Tuple(types ...Type) Type {
	return tupleT(types)
}

// Array returns a new Array type of the given underlying type.
func Array(underlying Type) Type {
	return arrayT{underlying}
}

// MysqlTypeToType gets the column type using the mysql type
func MysqlTypeToType(sql query.Type) (Type, error) {
	switch sql {
	case sqltypes.Null:
		return Null, nil
	case sqltypes.Int8:
		return Int8, nil
	case sqltypes.Uint8:
		return Uint8, nil
	case sqltypes.Int16:
		return Int16, nil
	case sqltypes.Uint16:
		return Uint16, nil
	case sqltypes.Int32:
		return Int32, nil
	case sqltypes.Int64:
		return Int64, nil
	case sqltypes.Uint32:
		return Uint32, nil
	case sqltypes.Uint64:
		return Uint64, nil
	case sqltypes.Float32:
		return Float32, nil
	case sqltypes.Float64:
		return Float64, nil
	case sqltypes.Timestamp:
		return Timestamp, nil
	case sqltypes.Date:
		return Date, nil
	case sqltypes.Text, sqltypes.VarChar:
		return Text, nil
	case sqltypes.Bit:
		return Boolean, nil
	case sqltypes.TypeJSON:
		return JSON, nil
	case sqltypes.Blob:
		return Blob, nil
	default:
		return nil, ErrTypeNotSupported.New(sql)
	}
}

type nullT struct{}

func (t nullT) String() string { return "NULL" }

// Type implements Type interface.
func (t nullT) Type() query.Type {
	return sqltypes.Null
}

// SQL implements Type interface.
func (t nullT) SQL(interface{}) (sqltypes.Value, error) {
	return sqltypes.NULL, nil
}

// Convert implements Type interface.
func (t nullT) Convert(v interface{}) (interface{}, error) {
	if v != nil {
		return nil, ErrValueNotNil.New(v)
	}

	return nil, nil
}

// Compare implements Type interface. Note that while this returns 0 (equals)
// for ordering purposes, in SQL NULL != NULL.
func (t nullT) Compare(a interface{}, b interface{}) (int, error) {
	return 0, nil
}

// IsNull returns true if expression is nil or is Null Type, otherwise false.
func IsNull(ex Expression) bool {
	return ex == nil || ex.Type() == Null
}

type numberT struct {
	t query.Type
}

// Type implements Type interface.
func (t numberT) Type() query.Type {
	return t.t
}

// SQL implements Type interface.
func (t numberT) SQL(v interface{}) (sqltypes.Value, error) {
	if _, ok := v.(nullT); ok {
		return sqltypes.NULL, nil
	}

	switch t.t {
	case sqltypes.Int32:
		return sqltypes.MakeTrusted(t.t, strconv.AppendInt(nil, cast.ToInt64(v), 10)), nil
	case sqltypes.Int64:
		return sqltypes.MakeTrusted(t.t, strconv.AppendInt(nil, cast.ToInt64(v), 10)), nil
	case sqltypes.Uint32:
		return sqltypes.MakeTrusted(t.t, strconv.AppendUint(nil, cast.ToUint64(v), 10)), nil
	case sqltypes.Uint64:
		return sqltypes.MakeTrusted(t.t, strconv.AppendUint(nil, cast.ToUint64(v), 10)), nil
	case sqltypes.Float32:
		return sqltypes.MakeTrusted(t.t, strconv.AppendFloat(nil, cast.ToFloat64(v), 'f', -1, 64)), nil
	case sqltypes.Float64:
		return sqltypes.MakeTrusted(t.t, strconv.AppendFloat(nil, cast.ToFloat64(v), 'f', -1, 64)), nil
	default:
		return sqltypes.MakeTrusted(t.t, []byte{}), nil
	}
}

// Convert implements Type interface.
func (t numberT) Convert(v interface{}) (interface{}, error) {
	if ti, ok := v.(time.Time); ok {
		v = ti.Unix()
	}

	switch t.t {
	case sqltypes.Int32:
		return cast.ToInt32E(v)
	case sqltypes.Int64:
		return cast.ToInt64E(v)
	case sqltypes.Uint32:
		return cast.ToUint32E(v)
	case sqltypes.Uint64:
		return cast.ToUint64E(v)
	case sqltypes.Float32:
		return cast.ToFloat32E(v)
	case sqltypes.Float64:
		return cast.ToFloat64E(v)
	default:
		return nil, ErrInvalidType.New(t.t)
	}
}

// Compare implements Type interface.
func (t numberT) Compare(a interface{}, b interface{}) (int, error) {
	if IsUnsigned(t) {
		return compareUnsigned(a, b)
	}

	return compareSigned(a, b)
}

func (t numberT) String() string { return t.t.String() }

func compareSigned(a interface{}, b interface{}) (int, error) {
	ca, err := cast.ToInt64E(a)
	if err != nil {
		return 0, err
	}
	cb, err := cast.ToInt64E(b)
	if err != nil {
		return 0, err
	}

	if ca == cb {
		return 0, nil
	}

	if ca < cb {
		return -1, nil
	}

	return +1, nil
}

func compareUnsigned(a interface{}, b interface{}) (int, error) {
	ca, err := cast.ToUint64E(a)
	if err != nil {
		return 0, err
	}
	cb, err := cast.ToUint64E(b)
	if err != nil {
		return 0, err
	}

	if ca == cb {
		return 0, nil
	}

	if ca < cb {
		return -1, nil
	}

	return +1, nil
}

type timestampT struct{}

func (t timestampT) String() string { return "TIMESTAMP" }

// Type implements Type interface.
func (t timestampT) Type() query.Type {
	return sqltypes.Timestamp
}

// TimestampLayout is the formatting string with the layout of the timestamp
// using the format of Go "time" package.
const TimestampLayout = "2006-01-02 15:04:05"

// TimestampLayouts hold extra timestamps allowed for parsing. It does
// not have all the layouts supported by mysql. Missing are two digit year
// versions of common cases and dates that use non common separators.
//
// https://github.com/MariaDB/server/blob/mysql-5.5.36/sql-common/my_time.c#L124
var TimestampLayouts = []string{
	"2006-01-02",
	time.RFC3339,
	"20060102150405",
	"20060102",
}

// SQL implements Type interface.
func (t timestampT) SQL(v interface{}) (sqltypes.Value, error) {
	if _, ok := v.(nullT); ok {
		return sqltypes.NULL, nil
	}

	v, err := t.Convert(v)
	if err != nil {
		return sqltypes.Value{}, err
	}

	return sqltypes.MakeTrusted(
		sqltypes.Timestamp,
		[]byte(v.(time.Time).Format(TimestampLayout)),
	), nil
}

// Convert implements Type interface.
func (t timestampT) Convert(v interface{}) (interface{}, error) {
	switch value := v.(type) {
	case time.Time:
		return value.UTC(), nil
	case string:
		t, err := time.Parse(TimestampLayout, value)
		if err != nil {
			failed := true
			for _, fmt := range TimestampLayouts {
				if t2, err2 := time.Parse(fmt, value); err2 == nil {
					t = t2
					failed = false
					break
				}
			}

			if failed {
				return nil, ErrConvertingToTime.Wrap(err, v)
			}
		}
		return t.UTC(), nil
	default:
		ts, err := Int64.Convert(v)
		if err != nil {
			return nil, ErrInvalidType.New(reflect.TypeOf(v))
		}

		return time.Unix(ts.(int64), 0).UTC(), nil
	}
}

// Compare implements Type interface.
func (t timestampT) Compare(a interface{}, b interface{}) (int, error) {
	av := a.(time.Time)
	bv := b.(time.Time)
	if av.Before(bv) {
		return -1, nil
	} else if av.After(bv) {
		return 1, nil
	}
	return 0, nil
}

type dateT struct{}

// DateLayout is the layout of the MySQL date format in the representation
// Go understands.
const DateLayout = "2006-01-02"

func truncateDate(t time.Time) time.Time {
	return t.Truncate(24 * time.Hour)
}

func (t dateT) String() string { return "DATE" }

func (t dateT) Type() query.Type {
	return sqltypes.Date
}

func (t dateT) SQL(v interface{}) (sqltypes.Value, error) {
	if _, ok := v.(nullT); ok {
		return sqltypes.NULL, nil
	}

	v, err := t.Convert(v)
	if err != nil {
		return sqltypes.Value{}, err
	}

	return sqltypes.MakeTrusted(
		sqltypes.Timestamp,
		[]byte(v.(time.Time).Format(DateLayout)),
	), nil
}

func (t dateT) Convert(v interface{}) (interface{}, error) {
	switch value := v.(type) {
	case time.Time:
		return truncateDate(value).UTC(), nil
	case string:
		t, err := time.Parse(DateLayout, value)
		if err != nil {
			return nil, ErrConvertingToTime.Wrap(err, v)
		}
		return truncateDate(t).UTC(), nil
	default:
		ts, err := Int64.Convert(v)
		if err != nil {
			return nil, ErrInvalidType.New(reflect.TypeOf(v))
		}

		return truncateDate(time.Unix(ts.(int64), 0)).UTC(), nil
	}
}

func (t dateT) Compare(a, b interface{}) (int, error) {
	av := truncateDate(a.(time.Time))
	bv := truncateDate(b.(time.Time))
	if av.Before(bv) {
		return -1, nil
	} else if av.After(bv) {
		return 1, nil
	}
	return 0, nil
}

type textT struct{}

func (t textT) String() string { return "TEXT" }

// Type implements Type interface.
func (t textT) Type() query.Type {
	return sqltypes.Text
}

// SQL implements Type interface.
func (t textT) SQL(v interface{}) (sqltypes.Value, error) {
	if _, ok := v.(nullT); ok {
		return sqltypes.NULL, nil
	}

	v, err := t.Convert(v)
	if err != nil {
		return sqltypes.Value{}, err
	}

	return sqltypes.MakeTrusted(sqltypes.Text, []byte(v.(string))), nil
}

// Convert implements Type interface.
func (t textT) Convert(v interface{}) (interface{}, error) {
	val, err := cast.ToStringE(v)
	if err != nil {
		return nil, ErrConvertToSQL.New(t)
	}
	return val, nil
}

// Compare implements Type interface.
func (t textT) Compare(a interface{}, b interface{}) (int, error) {
	return strings.Compare(a.(string), b.(string)), nil
}

type booleanT struct{}

func (t booleanT) String() string { return "BOOLEAN" }

// Type implements Type interface.
func (t booleanT) Type() query.Type {
	return sqltypes.Bit
}

// SQL implements Type interface.
func (t booleanT) SQL(v interface{}) (sqltypes.Value, error) {
	if _, ok := v.(nullT); ok {
		return sqltypes.NULL, nil
	}

	b := []byte{'0'}
	if cast.ToBool(v) {
		b[0] = '1'
	}

	return sqltypes.MakeTrusted(sqltypes.Bit, b), nil
}

// Convert implements Type interface.
func (t booleanT) Convert(v interface{}) (interface{}, error) {
	switch b := v.(type) {
	case bool:
		return b, nil
	case int, int64, int32, int16, int8, uint, uint64, uint32, uint16, uint8:
		if b != 0 {
			return true, nil
		}
		return false, nil
	case time.Duration:
		if int64(b) != 0 {
			return true, nil
		}
		return false, nil
	case time.Time:
		if b.UnixNano() != 0 {
			return true, nil
		}
		return false, nil
	case float32, float64:
		if int(math.Round(v.(float64))) != 0 {
			return true, nil
		}
		return false, nil
	case string:
		return false, fmt.Errorf("unable to cast string to bool")

	case nil:
		return nil, fmt.Errorf("unable to cast nil to bool")

	default:
		return nil, fmt.Errorf("unable to cast %#v of type %T to bool", v, v)
	}
}

// Compare implements Type interface.
func (t booleanT) Compare(a interface{}, b interface{}) (int, error) {
	if a == b {
		return 0, nil
	}

	if a == false {
		return -1, nil
	}

	return 1, nil
}

type blobT struct{}

func (t blobT) String() string { return "BLOB" }

// Type implements Type interface.
func (t blobT) Type() query.Type {
	return sqltypes.Blob
}

// SQL implements Type interface.
func (t blobT) SQL(v interface{}) (sqltypes.Value, error) {
	if _, ok := v.(nullT); ok {
		return sqltypes.NULL, nil
	}

	v, err := t.Convert(v)
	if err != nil {
		return sqltypes.Value{}, err
	}

	return sqltypes.MakeTrusted(sqltypes.Blob, v.([]byte)), nil
}

// Convert implements Type interface.
func (t blobT) Convert(v interface{}) (interface{}, error) {
	switch value := v.(type) {
	case nil:
		return []byte(nil), nil
	case []byte:
		return value, nil
	case string:
		return []byte(value), nil
	case fmt.Stringer:
		return []byte(value.String()), nil
	default:
		return nil, ErrInvalidType.New(reflect.TypeOf(v))
	}
}

// Compare implements Type interface.
func (t blobT) Compare(a interface{}, b interface{}) (int, error) {
	return bytes.Compare(a.([]byte), b.([]byte)), nil
}

type jsonT struct{}

func (t jsonT) String() string { return "JSON" }

// Type implements Type interface.
func (t jsonT) Type() query.Type {
	return sqltypes.TypeJSON
}

// SQL implements Type interface.
func (t jsonT) SQL(v interface{}) (sqltypes.Value, error) {
	if _, ok := v.(nullT); ok {
		return sqltypes.NULL, nil
	}

	v, err := t.Convert(v)
	if err != nil {
		return sqltypes.Value{}, err
	}

	return sqltypes.MakeTrusted(sqltypes.TypeJSON, v.([]byte)), nil
}

// Convert implements Type interface.
func (t jsonT) Convert(v interface{}) (interface{}, error) {
	switch v := v.(type) {
	case string:
		var doc interface{}
		if err := json.Unmarshal([]byte(v), &doc); err != nil {
			return json.Marshal(v)
		}
		return json.Marshal(doc)
	default:
		return json.Marshal(v)
	}
}

// Compare implements Type interface.
func (t jsonT) Compare(a interface{}, b interface{}) (int, error) {
	return bytes.Compare(a.([]byte), b.([]byte)), nil
}

type tupleT []Type

func (t tupleT) String() string {
	var elems = make([]string, len(t))
	for i, el := range t {
		elems[i] = el.String()
	}
	return fmt.Sprintf("TUPLE(%s)", strings.Join(elems, ", "))
}

func (t tupleT) Type() query.Type {
	return sqltypes.Expression
}

func (t tupleT) SQL(v interface{}) (sqltypes.Value, error) {
	if _, ok := v.(nullT); ok {
		return sqltypes.NULL, nil
	}

	return sqltypes.Value{}, fmt.Errorf("unable to convert tuple type to SQL")
}

func (t tupleT) Convert(v interface{}) (interface{}, error) {
	if vals, ok := v.([]interface{}); ok {
		if len(vals) != len(t) {
			return nil, ErrInvalidColumnNumber.New(len(t), len(vals))
		}

		var result = make([]interface{}, len(t))
		for i, typ := range t {
			var err error
			result[i], err = typ.Convert(vals[i])
			if err != nil {
				return nil, err
			}
		}

		return result, nil
	}
	return nil, ErrNotTuple.New(v)
}

func (t tupleT) Compare(a, b interface{}) (int, error) {
	a, err := t.Convert(a)
	if err != nil {
		return 0, err
	}

	b, err = t.Convert(b)
	if err != nil {
		return 0, err
	}

	left := a.([]interface{})
	right := b.([]interface{})
	for i := range left {
		cmp, err := t[i].Compare(left[i], right[i])
		if err != nil {
			return 0, err
		}

		if cmp != 0 {
			return cmp, nil
		}
	}

	return 0, nil
}

type arrayT struct {
	underlying Type
}

func (t arrayT) String() string { return fmt.Sprintf("ARRAY(%s)", t.underlying) }

func (t arrayT) Type() query.Type {
	return sqltypes.TypeJSON
}

func (t arrayT) SQL(v interface{}) (sqltypes.Value, error) {
	if _, ok := v.(nullT); ok {
		return sqltypes.NULL, nil
	}

	v, err := t.Convert(v)
	if err != nil {
		return sqltypes.Value{}, err
	}

	return JSON.SQL(v)
}

func (t arrayT) Convert(v interface{}) (interface{}, error) {
	switch v := v.(type) {
	case []interface{}:
		var result = make([]interface{}, len(v))
		for i, v := range v {
			var err error
			result[i], err = t.underlying.Convert(v)
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	case Generator:
		var values []interface{}
		for {
			val, err := v.Next()
			if err != nil {
				if err == io.EOF {
					break
				}
				return nil, err
			}

			val, err = t.underlying.Convert(val)
			if err != nil {
				return nil, err
			}

			values = append(values, val)
		}

		if err := v.Close(); err != nil {
			return nil, err
		}

		return values, nil
	default:
		return nil, ErrNotArray.New(v)
	}
}

func (t arrayT) Compare(a, b interface{}) (int, error) {
	a, err := t.Convert(a)
	if err != nil {
		return 0, err
	}

	b, err = t.Convert(b)
	if err != nil {
		return 0, err
	}

	left := a.([]interface{})
	right := b.([]interface{})

	if len(left) < len(right) {
		return -1, nil
	} else if len(left) > len(right) {
		return 1, nil
	}

	for i := range left {
		cmp, err := t.underlying.Compare(left[i], right[i])
		if err != nil {
			return 0, err
		}

		if cmp != 0 {
			return cmp, nil
		}
	}

	return 0, nil
}

// IsNumber checks if t is a number type
func IsNumber(t Type) bool {
	return IsInteger(t) || IsDecimal(t)
}

// IsSigned checks if t is a signed type.
func IsSigned(t Type) bool {
	return t == Int32 || t == Int64
}

// IsUnsigned checks if t is an unsigned type.
func IsUnsigned(t Type) bool {
	return t == Uint32 || t == Uint64
}

// IsInteger checks if t is a (U)Int32/64 type.
func IsInteger(t Type) bool {
	return IsSigned(t) || IsUnsigned(t)
}

// IsTime checks if t is a timestamp or date.
func IsTime(t Type) bool {
	return t == Timestamp || t == Date
}

// IsDecimal checks if t is decimal type.
func IsDecimal(t Type) bool {
	return t == Float32 || t == Float64
}

// IsText checks if t is a text type.
func IsText(t Type) bool {
	return t == Text || t == Blob || t == JSON
}

// IsTuple checks if t is a tuple type.
// Note that tupleT instances with just 1 value are not considered
// as a tuple, but a parenthesized value.
func IsTuple(t Type) bool {
	v, ok := t.(tupleT)
	return ok && len(v) > 1
}

// IsArray returns whether the given type is an array.
func IsArray(t Type) bool {
	_, ok := t.(arrayT)
	return ok
}

// NumColumns returns the number of columns in a type. This is one for all
// types, except tuples.
func NumColumns(t Type) int {
	v, ok := t.(tupleT)
	if !ok {
		return 1
	}
	return len(v)
}

// MySQLTypeName returns the MySQL display name for the given type.
func MySQLTypeName(t Type) string {
	switch t.Type() {
	case sqltypes.Int8:
		return "TINYINT"
	case sqltypes.Uint8:
		return "TINYINT UNSIGNED"
	case sqltypes.Int16:
		return "SMALLINT"
	case sqltypes.Uint16:
		return "SMALLINT UNSIGNED"
	case sqltypes.Int32:
		return "INTEGER"
	case sqltypes.Int64:
		return "BIGINT"
	case sqltypes.Uint32:
		return "INTEGER UNSIGNED"
	case sqltypes.Uint64:
		return "BIGINT UNSIGNED"
	case sqltypes.Float32:
		return "FLOAT"
	case sqltypes.Float64:
		return "DOUBLE"
	case sqltypes.Timestamp:
		return "DATETIME"
	case sqltypes.Date:
		return "DATE"
	case sqltypes.Text, sqltypes.VarChar:
		return "TEXT"
	case sqltypes.Bit:
		return "BIT"
	case sqltypes.TypeJSON:
		return "JSON"
	case sqltypes.Blob:
		return "BLOB"
	default:
		return "UNKNOWN"
	}
}

// UnderlyingType returns the underlying type of an array if the type is an
// array, or the type itself in any other case.
func UnderlyingType(t Type) Type {
	a, ok := t.(arrayT)
	if !ok {
		return t
	}

	return a.underlying
}
