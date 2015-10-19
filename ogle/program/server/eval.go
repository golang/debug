// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Evaluates Go expressions, using the current values of variables in a program
// being debugged.
//
// TODOs:
// More overflow checking.
// Stricter type checking.
// More expression types.

package server

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"math/big"

	"golang.org/x/debug/dwarf"
	"golang.org/x/debug/ogle/program"
)

const prec = 256 // precision for untyped float and complex constants.

var (
	// Some big.Ints to use in overflow checks.
	bigIntMaxInt32 = new(big.Int).SetInt64(math.MaxInt32)
	bigIntMinInt32 = new(big.Int).SetInt64(math.MinInt32)
	bigIntMaxInt64 = new(big.Int).SetInt64(math.MaxInt64)
	bigIntMinInt64 = new(big.Int).SetInt64(math.MinInt64)
)

// result stores an intermediate value produced during evaluation of an expression.
//
// d contains the DWARF type of the value.  For untyped values, d will be nil.
//
// v contains the value itself.  For numeric and bool types, v will have the
// corresponding predeclared Go type.
// For untyped integer, rune, float, complex, string, and bool constants, v will
// have type untInt, untRune, untFloat, untComplex, untString, or bool,
// respectively.
// For values of type int, uint and uintptr, v will be an int32, int64, uint32
// or uint64 as appropriate.
// For address operations, v will have type pointerToValue.
// For the operands of address operations, v will have type addressableValue.
// Other types are represented using the corresponding implementation of
// program.Value in program.go.
//
// If an evaluation results in an error, the zero value of result is used.
type result struct {
	d dwarf.Type
	v interface{}
}

// untInt is an untyped integer constant
type untInt struct {
	*big.Int
}

// untRune is an untyped rune constant
type untRune struct {
	*big.Int
}

// untFloat is an untyped floating-point constant
type untFloat struct {
	*big.Float
}

// untComplex is an untyped complex constant
type untComplex struct {
	r *big.Float
	i *big.Float
}

// untString is an untyped string constant
type untString string

// pointerToValue is a pointer to a value in memory.
// The evaluator constructs these as the result of address operations like "&x".
// Unlike program.Pointer, the DWARF type stored alongside values of this type
// is the type of the variable, not the type of the pointer.
type pointerToValue struct {
	a uint64
}

// addressableValue is the memory location of a value.
// The evaluator constructs these while evaluating the operands of address
// operations like "&x", instead of computing the value of x itself.
type addressableValue struct {
	a uint64
}

// evalExpression evaluates a Go expression.
// If the program counter and stack pointer are nonzero, they are used to determine
// what local variables are available and where in memory they are.
func (s *Server) evalExpression(expression string, pc, sp uint64) (program.Value, error) {
	e := evaluator{server: s, expression: expression, pc: pc, sp: sp}
	node, err := parser.ParseExpr(expression)
	if err != nil {
		return nil, err
	}
	val := e.evalNode(node, false)
	if e.evalError != nil {
		return nil, e.evalError
	}

	// Convert untyped constants to their default types.
	switch v := val.v.(type) {
	case untInt:
		return e.intFromInteger(v)
	case untRune:
		if v.Cmp(bigIntMaxInt32) == +1 {
			return nil, errors.New("constant overflows rune")
		}
		if v.Cmp(bigIntMinInt32) == -1 {
			return nil, errors.New("constant overflows rune")
		}
		return int32(v.Int64()), nil
	case untFloat:
		f, _ := v.Float64()
		if math.IsInf(f, 0) {
			return nil, errors.New("constant overflows float64")
		}
		if math.IsNaN(f) {
			return nil, errors.New("constant is NaN")
		}
		return f, nil
	case untComplex:
		r, _ := v.r.Float64()
		i, _ := v.i.Float64()
		if math.IsInf(r, 0) || math.IsInf(i, 0) {
			return nil, errors.New("constant overflows complex128")
		}
		if math.IsNaN(r) || math.IsNaN(i) {
			return nil, errors.New("constant is NaN")
		}
		return complex(r, i), nil
	case untString:
		return program.String{Length: uint64(len(v)), String: string(v)}, nil
	case pointerToValue:
		return program.Pointer{TypeID: uint64(val.d.Common().Offset), Address: v.a}, nil
	case nil, addressableValue:
		// This case should not be reachable.
		return nil, errors.New("unknown error")
	}
	return val.v, nil
}

type evaluator struct {
	// expression is the expression being evaluated.
	expression string
	// server interacts with the program being debugged.
	server *Server
	// curNode is the current parse tree node.  This is set so that error messages
	// can quote the part of the expression that caused an error.
	curNode ast.Node
	// evalError is the first error that occurred while evaluating the expression,
	// or nil if no error has occurred.
	evalError error
	// pc and sp are the current program counter and stack pointer, used for
	// finding local variables.  If either are zero, the expression is evaluated
	// without using local variables.
	pc uint64
	sp uint64
}

// setNode sets curNode, and returns curNode's previous value.
func (e *evaluator) setNode(node ast.Node) (old ast.Node) {
	old, e.curNode = e.curNode, node
	return old
}

// err saves an error that occurred during evaluation.
// It returns a zero result, so that functions can exit and set an error with
//	return e.err(...)
func (e *evaluator) err(s string) result {
	if e.evalError != nil {
		return result{}
	}
	// Append the substring of the expression that corresponds to the current AST node.
	start := int(e.curNode.Pos() - 1)
	end := int(e.curNode.End() - 1)
	if start < 0 {
		start = 0
	}
	if end > len(e.expression) {
		end = len(e.expression)
	}
	if start > end {
		start, end = 0, 0
	}
	e.evalError = errors.New(s + `: "` + e.expression[start:end] + `"`)
	return result{}
}

// evalNode computes the value of a node in the expression tree.
// If getAddress is true, the node is the argument of an & operator, so evalNode
// will return a result with a value of type addressableValue if possible.
func (e *evaluator) evalNode(node ast.Node, getAddress bool) result {
	// Set the current node in the evaluator, so that error messages can refer to
	// it.  Defer a function call that changes it back.
	defer e.setNode(e.setNode(node))

	switch n := node.(type) {
	case *ast.Ident:
		if e.pc != 0 && e.sp != 0 {
			a, t := e.server.findLocalVar(n.Name, e.pc, e.sp)
			if t != nil {
				return e.resultFrom(a, t, getAddress)
			}
		}
		a, t := e.server.findGlobalVar(n.Name)
		if t != nil {
			return e.resultFrom(a, t, getAddress)
		}
		switch n.Name {
		// Note: these could have been redefined as constants in the code, but we
		// don't have a way to detect that.
		case "true":
			return result{nil, true}
		case "false":
			return result{nil, false}
		}
		return e.err("unknown identifier")

	case *ast.BasicLit:
		switch n.Kind {
		case token.INT:
			i := new(big.Int)
			if _, ok := i.SetString(n.Value, 0); !ok {
				return e.err("invalid integer constant")
			}
			return result{nil, untInt{i}}
		case token.FLOAT:
			r, _, err := big.ParseFloat(n.Value, 10, prec, big.ToNearestEven)
			if err != nil {
				return e.err(err.Error())
			}
			return result{nil, untFloat{r}}
		case token.IMAG:
			if len(n.Value) <= 1 || n.Value[len(n.Value)-1] != 'i' {
				return e.err("invalid imaginary constant")
			}
			r, _, err := big.ParseFloat(n.Value[:len(n.Value)-1], 10, prec, big.ToNearestEven)
			if err != nil {
				return e.err(err.Error())
			}
			return result{nil, untComplex{new(big.Float), r}}
		case token.CHAR:
			// TODO: unescaping
			return result{nil, untRune{new(big.Int).SetInt64(int64(n.Value[1]))}}
		case token.STRING:
			// TODO: unescaping
			if len(n.Value) <= 1 {
				return e.err("invalid string constant")
			}
			return result{nil, untString(n.Value[1 : len(n.Value)-1])}
		}

	case *ast.ParenExpr:
		return e.evalNode(n.X, getAddress)

	case *ast.StarExpr:
		x := e.evalNode(n.X, false)
		switch v := x.v.(type) {
		case program.Pointer:
			// x.d may be a typedef pointing to a pointer type (or a typedef pointing
			// to a typedef pointing to a pointer type, etc.), so remove typedefs
			// until we get the underlying pointer type.
			t := followTypedefs(x.d)
			if pt, ok := t.(*dwarf.PtrType); ok {
				return e.resultFrom(v.Address, pt.Type, getAddress)
			} else {
				e.err("invalid pointer type")
			}
		case pointerToValue:
			return e.resultFrom(v.a, x.d, getAddress)
		case nil:
			return x
		}
		return e.err("invalid indirect")

	case *ast.UnaryExpr:
		if n.Op == token.AND {
			x := e.evalNode(n.X, true)
			switch v := x.v.(type) {
			case addressableValue:
				return result{x.d, pointerToValue{v.a}}
			case nil:
				return x
			}
			return e.err("can't take address")
		}

		x := e.evalNode(n.X, false)
		if x.v == nil {
			return x
		}
		switch v := x.v.(type) {

		case int8:
			switch n.Op {
			case token.ADD:
			case token.SUB:
				v = -v
			case token.XOR:
				v = ^v
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}

		case int16:
			switch n.Op {
			case token.ADD:
			case token.SUB:
				v = -v
			case token.XOR:
				v = ^v
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}

		case int32:
			switch n.Op {
			case token.ADD:
			case token.SUB:
				v = -v
			case token.XOR:
				v = ^v
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}

		case int64:
			switch n.Op {
			case token.ADD:
			case token.SUB:
				v = -v
			case token.XOR:
				v = ^v
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}

		case uint8:
			switch n.Op {
			case token.ADD:
			case token.SUB:
				v = -v
			case token.XOR:
				v = ^v
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}

		case uint16:
			switch n.Op {
			case token.ADD:
			case token.SUB:
				v = -v
			case token.XOR:
				v = ^v
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}

		case uint32:
			switch n.Op {
			case token.ADD:
			case token.SUB:
				v = -v
			case token.XOR:
				v = ^v
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}

		case uint64:
			switch n.Op {
			case token.ADD:
			case token.SUB:
				v = -v
			case token.XOR:
				v = ^v
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}

		case float32:
			switch n.Op {
			case token.ADD:
			case token.SUB:
				v = -v
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}

		case float64:
			switch n.Op {
			case token.ADD:
			case token.SUB:
				v = -v
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}

		case complex64:
			switch n.Op {
			case token.ADD:
			case token.SUB:
				v = -v
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}

		case complex128:
			switch n.Op {
			case token.ADD:
			case token.SUB:
				v = -v
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}

		case untInt:
			switch n.Op {
			case token.ADD:
			case token.SUB:
				v.Int.Neg(v.Int)
			case token.XOR:
				v.Int.Not(v.Int)
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}

		case untRune:
			switch n.Op {
			case token.ADD:
			case token.SUB:
				v.Int.Neg(v.Int)
			case token.XOR:
				v.Int.Not(v.Int)
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}

		case untFloat:
			switch n.Op {
			case token.ADD:
			case token.SUB:
				v.Float.Neg(v.Float)
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}

		case untComplex:
			switch n.Op {
			case token.ADD:
			case token.SUB:
				v.r.Neg(v.r)
				v.i.Neg(v.i)
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}

		case bool:
			switch n.Op {
			case token.NOT:
				v = !v
			default:
				return e.err("invalid operation")
			}
			return result{x.d, v}
		}

	case *ast.BinaryExpr:
		x := e.evalNode(n.X, false)
		if x.v == nil {
			return x
		}
		y := e.evalNode(n.Y, false)
		if y.v == nil {
			return y
		}
		return e.evalBinaryOp(n.Op, x, y)
	}
	return e.err("invalid expression")
}

// evalBinaryOp evaluates a binary operator op applied to x and y.
func (e *evaluator) evalBinaryOp(op token.Token, x, y result) result {
	if op == token.NEQ {
		tmp := e.evalBinaryOp(token.EQL, x, y)
		b, ok := tmp.v.(bool)
		if !ok {
			return tmp
		}
		return result{nil, !b}
	}
	if op == token.GTR {
		return e.evalBinaryOp(token.LSS, y, x)
	}
	if op == token.GEQ {
		return e.evalBinaryOp(token.LEQ, x, y)
	}

	x = convertUntyped(x, y)
	y = convertUntyped(y, x)

	switch a := x.v.(type) {

	case int8:
		b, ok := y.v.(int8)
		if !ok {
			return e.err("type mismatch")
		}
		var c int8
		switch op {
		case token.EQL:
			return result{nil, a == b}
		case token.LSS:
			return result{nil, a < b}
		case token.LEQ:
			return result{nil, a <= b}
		case token.ADD:
			c = a + b
		case token.SUB:
			c = a - b
		case token.OR:
			c = a | b
		case token.XOR:
			c = a ^ b
		case token.MUL:
			c = a * b
		case token.QUO:
			if b == 0 {
				return e.err("integer divide by zero")
			}
			c = a / b
		case token.REM:
			if b == 0 {
				return e.err("integer divide by zero")
			}
			c = a % b
		case token.AND:
			c = a & b
		case token.AND_NOT:
			c = a &^ b
		default:
			return e.err("invalid operation")
		}
		return result{x.d, c}

	case int16:
		b, ok := y.v.(int16)
		if !ok {
			return e.err("type mismatch")
		}
		var c int16
		switch op {
		case token.EQL:
			return result{nil, a == b}
		case token.LSS:
			return result{nil, a < b}
		case token.LEQ:
			return result{nil, a <= b}
		case token.ADD:
			c = a + b
		case token.SUB:
			c = a - b
		case token.OR:
			c = a | b
		case token.XOR:
			c = a ^ b
		case token.MUL:
			c = a * b
		case token.QUO:
			if b == 0 {
				return e.err("integer divide by zero")
			}
			c = a / b
		case token.REM:
			if b == 0 {
				return e.err("integer divide by zero")
			}
			c = a % b
		case token.AND:
			c = a & b
		case token.AND_NOT:
			c = a &^ b
		default:
			return e.err("invalid operation")
		}
		return result{x.d, c}

	case int32:
		b, ok := y.v.(int32)
		if !ok {
			return e.err("type mismatch")
		}
		var c int32
		switch op {
		case token.EQL:
			return result{nil, a == b}
		case token.LSS:
			return result{nil, a < b}
		case token.LEQ:
			return result{nil, a <= b}
		case token.ADD:
			c = a + b
		case token.SUB:
			c = a - b
		case token.OR:
			c = a | b
		case token.XOR:
			c = a ^ b
		case token.MUL:
			c = a * b
		case token.QUO:
			if b == 0 {
				return e.err("integer divide by zero")
			}
			c = a / b
		case token.REM:
			if b == 0 {
				return e.err("integer divide by zero")
			}
			c = a % b
		case token.AND:
			c = a & b
		case token.AND_NOT:
			c = a &^ b
		default:
			return e.err("invalid operation")
		}
		return result{x.d, c}

	case int64:
		b, ok := y.v.(int64)
		if !ok {
			return e.err("type mismatch")
		}
		var c int64
		switch op {
		case token.EQL:
			return result{nil, a == b}
		case token.LSS:
			return result{nil, a < b}
		case token.LEQ:
			return result{nil, a <= b}
		case token.ADD:
			c = a + b
		case token.SUB:
			c = a - b
		case token.OR:
			c = a | b
		case token.XOR:
			c = a ^ b
		case token.MUL:
			c = a * b
		case token.QUO:
			if b == 0 {
				return e.err("integer divide by zero")
			}
			c = a / b
		case token.REM:
			if b == 0 {
				return e.err("integer divide by zero")
			}
			c = a % b
		case token.AND:
			c = a & b
		case token.AND_NOT:
			c = a &^ b
		default:
			return e.err("invalid operation")
		}
		return result{x.d, c}

	case uint8:
		b, ok := y.v.(uint8)
		if !ok {
			return e.err("type mismatch")
		}
		var c uint8
		switch op {
		case token.EQL:
			return result{nil, a == b}
		case token.LSS:
			return result{nil, a < b}
		case token.LEQ:
			return result{nil, a <= b}
		case token.ADD:
			c = a + b
		case token.SUB:
			c = a - b
		case token.OR:
			c = a | b
		case token.XOR:
			c = a ^ b
		case token.MUL:
			c = a * b
		case token.QUO:
			if b == 0 {
				return e.err("integer divide by zero")
			}
			c = a / b
		case token.REM:
			if b == 0 {
				return e.err("integer divide by zero")
			}
			c = a % b
		case token.AND:
			c = a & b
		case token.AND_NOT:
			c = a &^ b
		default:
			return e.err("invalid operation")
		}
		return result{x.d, c}

	case uint16:
		b, ok := y.v.(uint16)
		if !ok {
			return e.err("type mismatch")
		}
		var c uint16
		switch op {
		case token.EQL:
			return result{nil, a == b}
		case token.LSS:
			return result{nil, a < b}
		case token.LEQ:
			return result{nil, a <= b}
		case token.ADD:
			c = a + b
		case token.SUB:
			c = a - b
		case token.OR:
			c = a | b
		case token.XOR:
			c = a ^ b
		case token.MUL:
			c = a * b
		case token.QUO:
			if b == 0 {
				return e.err("integer divide by zero")
			}
			c = a / b
		case token.REM:
			if b == 0 {
				return e.err("integer divide by zero")
			}
			c = a % b
		case token.AND:
			c = a & b
		case token.AND_NOT:
			c = a &^ b
		default:
			return e.err("invalid operation")
		}
		return result{x.d, c}

	case uint32:
		b, ok := y.v.(uint32)
		if !ok {
			return e.err("type mismatch")
		}
		var c uint32
		switch op {
		case token.EQL:
			return result{nil, a == b}
		case token.LSS:
			return result{nil, a < b}
		case token.LEQ:
			return result{nil, a <= b}
		case token.ADD:
			c = a + b
		case token.SUB:
			c = a - b
		case token.OR:
			c = a | b
		case token.XOR:
			c = a ^ b
		case token.MUL:
			c = a * b
		case token.QUO:
			if b == 0 {
				return e.err("integer divide by zero")
			}
			c = a / b
		case token.REM:
			if b == 0 {
				return e.err("integer divide by zero")
			}
			c = a % b
		case token.AND:
			c = a & b
		case token.AND_NOT:
			c = a &^ b
		default:
			return e.err("invalid operation")
		}
		return result{x.d, c}

	case uint64:
		b, ok := y.v.(uint64)
		if !ok {
			return e.err("type mismatch")
		}
		var c uint64
		switch op {
		case token.EQL:
			return result{nil, a == b}
		case token.LSS:
			return result{nil, a < b}
		case token.LEQ:
			return result{nil, a <= b}
		case token.ADD:
			c = a + b
		case token.SUB:
			c = a - b
		case token.OR:
			c = a | b
		case token.XOR:
			c = a ^ b
		case token.MUL:
			c = a * b
		case token.QUO:
			if b == 0 {
				return e.err("integer divide by zero")
			}
			c = a / b
		case token.REM:
			if b == 0 {
				return e.err("integer divide by zero")
			}
			c = a % b
		case token.AND:
			c = a & b
		case token.AND_NOT:
			c = a &^ b
		default:
			return e.err("invalid operation")
		}
		return result{x.d, c}

	case float32:
		b, ok := y.v.(float32)
		if !ok {
			return e.err("type mismatch")
		}
		var c float32
		switch op {
		case token.EQL:
			return result{nil, a == b}
		case token.LSS:
			return result{nil, a < b}
		case token.LEQ:
			return result{nil, a <= b}
		case token.ADD:
			c = a + b
		case token.SUB:
			c = a - b
		case token.MUL:
			c = a * b
		case token.QUO:
			c = a / b
		default:
			return e.err("invalid operation")
		}
		return result{x.d, c}

	case float64:
		b, ok := y.v.(float64)
		if !ok {
			return e.err("type mismatch")
		}
		var c float64
		switch op {
		case token.EQL:
			return result{nil, a == b}
		case token.LSS:
			return result{nil, a < b}
		case token.LEQ:
			return result{nil, a <= b}
		case token.ADD:
			c = a + b
		case token.SUB:
			c = a - b
		case token.MUL:
			c = a * b
		case token.QUO:
			c = a / b
		default:
			return e.err("invalid operation")
		}
		return result{x.d, c}

	case complex64:
		b, ok := y.v.(complex64)
		if !ok {
			return e.err("type mismatch")
		}
		var c complex64
		switch op {
		case token.EQL:
			return result{nil, a == b}
		case token.ADD:
			c = a + b
		case token.SUB:
			c = a - b
		case token.MUL:
			c = a * b
		case token.QUO:
			c = a / b
		default:
			return e.err("invalid operation")
		}
		return result{x.d, c}

	case complex128:
		b, ok := y.v.(complex128)
		if !ok {
			return e.err("type mismatch")
		}
		var c complex128
		switch op {
		case token.EQL:
			return result{nil, a == b}
		case token.ADD:
			c = a + b
		case token.SUB:
			c = a - b
		case token.MUL:
			c = a * b
		case token.QUO:
			c = a / b
		default:
			return e.err("invalid operation")
		}
		return result{x.d, c}

	case bool:
		b, ok := y.v.(bool)
		if !ok {
			return e.err("type mismatch")
		}
		var c bool
		switch op {
		case token.LOR:
			c = a || b
		case token.LAND:
			c = a && b
		case token.EQL:
			c = a == b
		default:
			return e.err("invalid operation")
		}
		return result{x.d, c}

	case program.String:
		b, ok := y.v.(program.String)
		if !ok {
			return e.err("type mismatch")
		}
		var c program.String
		switch op {
		// TODO: these comparison operators only use the part of the string that
		// was read.  Very large strings do not have their entire contents read by
		// server.value.
		case token.EQL:
			return result{nil, a.Length == b.Length && a.String == b.String}
		case token.LSS:
			return result{nil, a.String < b.String}
		case token.LEQ:
			return result{nil, a.String <= b.String}
		case token.ADD:
			c.Length = a.Length + b.Length
			if a.Length == uint64(len(a.String)) {
				c.String = a.String + b.String
			} else {
				// The first string was truncated at a.Length characters, so the sum
				// must be truncated there too.
				c.String = a.String
			}
		default:
			return e.err("invalid operation")
		}
		return result{x.d, c}

	case untString:
		b, ok := y.v.(untString)
		if !ok {
			return e.err("type mismatch")
		}
		var c untString
		switch op {
		case token.EQL:
			return result{nil, a == b}
		case token.LSS:
			return result{nil, a < b}
		case token.LEQ:
			return result{nil, a <= b}
		case token.ADD:
			c = a + b
		default:
			return e.err("invalid operation")
		}
		return result{x.d, c}

	case untInt:
		i := a.Int
		b, ok := y.v.(untInt)
		if !ok {
			return e.err("type mismatch")
		}
		switch op {
		case token.EQL:
			return result{nil, i.Cmp(b.Int) == 0}
		case token.LSS:
			return result{nil, i.Cmp(b.Int) < 0}
		case token.LEQ:
			return result{nil, i.Cmp(b.Int) <= 0}
		}
		c := new(big.Int)
		switch op {
		case token.ADD:
			c.Add(i, b.Int)
		case token.SUB:
			c.Sub(i, b.Int)
		case token.OR:
			c.Or(i, b.Int)
		case token.XOR:
			c.Xor(i, b.Int)
		case token.MUL:
			c.Mul(i, b.Int)
		case token.QUO:
			if b.Sign() == 0 {
				return e.err("integer divide by zero")
			}
			c.Quo(i, b.Int)
		case token.REM:
			if b.Sign() == 0 {
				return e.err("integer divide by zero")
			}
			c.Mod(i, b.Int)
		case token.AND:
			c.And(i, b.Int)
		case token.AND_NOT:
			c.AndNot(i, b.Int)
		default:
			return e.err("invalid operation")
		}
		return result{nil, untInt{c}}

	case untRune:
		i := a.Int
		b, ok := y.v.(untRune)
		if !ok {
			return e.err("type mismatch")
		}
		switch op {
		case token.EQL:
			return result{nil, i.Cmp(b.Int) == 0}
		case token.LSS:
			return result{nil, i.Cmp(b.Int) < 0}
		case token.LEQ:
			return result{nil, i.Cmp(b.Int) <= 0}
		}
		c := new(big.Int)
		switch op {
		case token.ADD:
			c.Add(i, b.Int)
		case token.SUB:
			c.Sub(i, b.Int)
		case token.OR:
			c.Or(i, b.Int)
		case token.XOR:
			c.Xor(i, b.Int)
		case token.MUL:
			c.Mul(i, b.Int)
		case token.QUO:
			if b.Sign() == 0 {
				return e.err("integer divide by zero")
			}
			c.Quo(i, b.Int)
		case token.REM:
			if b.Sign() == 0 {
				return e.err("integer divide by zero")
			}
			c.Mod(i, b.Int)
		case token.AND:
			c.And(i, b.Int)
		case token.AND_NOT:
			c.AndNot(i, b.Int)
		default:
			return e.err("invalid operation")
		}
		return result{nil, untRune{c}}

	case untFloat:
		r := a.Float
		b, ok := y.v.(untFloat)
		if !ok {
			return e.err("type mismatch")
		}
		switch op {
		case token.EQL:
			return result{nil, r.Cmp(b.Float) == 0}
		case token.LSS:
			return result{nil, r.Cmp(b.Float) < 0}
		case token.LEQ:
			return result{nil, r.Cmp(b.Float) <= 0}
		}
		c := new(big.Float)
		switch op {
		case token.ADD:
			c.Add(r, b.Float)
		case token.SUB:
			c.Sub(r, b.Float)
		case token.MUL:
			c.Mul(r, b.Float)
		case token.QUO:
			if b.Sign() == 0 {
				return e.err("divide by zero")
			}
			c.Quo(r, b.Float)
		default:
			return e.err("invalid operation")
		}
		return result{nil, untFloat{c}}

	case untComplex:
		b, ok := y.v.(untComplex)
		if !ok {
			return e.err("type mismatch")
		}
		var (
			ar = a.r
			br = b.r
			ai = a.i
			bi = b.i
		)
		if op == token.EQL {
			return result{nil, ar.Cmp(br) == 0 && ai.Cmp(bi) == 0}
		}
		var (
			cr = new(big.Float)
			ci = new(big.Float)
		)
		switch op {
		case token.ADD:
			cr.Add(ar, br)
			ci.Add(ai, bi)
		case token.SUB:
			cr.Sub(ar, br)
			ci.Sub(ai, bi)
		case token.MUL:
			var t0, t1 big.Float
			t0.Mul(ar, br)
			t1.Mul(ai, bi)
			cr.Sub(&t0, &t1)
			t0.Mul(ar, bi)
			t1.Mul(ai, br)
			ci.Add(&t0, &t1)
		case token.QUO:
			// a/b = a*conj(b)/|b|^2
			var t0, t1 big.Float
			cr.Mul(ar, br)
			t0.Mul(ai, bi)
			cr.Add(cr, &t0) // cr = Re(a*conj(b))
			ci.Mul(ai, br)
			t0.Mul(ar, bi)
			ci.Sub(ci, &t0) // ci = Im(a*conj(b))
			t0.Mul(br, br)
			t1.Mul(bi, bi)
			t0.Add(&t0, &t1) // t0 = |b|^2
			if t0.Sign() == 0 {
				return e.err("divide by zero")
			}
			cr.Quo(cr, &t0) // cr = Re(a*conj(b))/|b|^2 = Re(a/b)
			ci.Quo(ci, &t0) // ci = Im(a*conj(b))/|b|^2 = Im(a/b)
		}
		return result{nil, untComplex{cr, ci}}
	}

	return e.err("invalid operation")
}

// findLocalVar finds a local variable (or function parameter) by name, and
// returns its address and DWARF type.  It returns a nil type on failure.
// The PC and SP are used to determine the current function and stack frame.
func (s *Server) findLocalVar(name string, pc, sp uint64) (uint64, dwarf.Type) {
	// Find the DWARF entry for the function at pc.
	funcEntry, _, err := s.entryForPC(uint64(pc))
	if err != nil {
		return 0, nil
	}

	// Compute the stack frame pointer.
	fpOffset, err := s.dwarfData.PCToSPOffset(uint64(pc))
	if err != nil {
		return 0, nil
	}
	framePointer := sp + uint64(fpOffset)

	// Check each child of the function's DWARF entry to see if it is a parameter
	// or local variable with the right name.  If so, return its address and type.
	r := s.dwarfData.Reader()
	r.Seek(funcEntry.Offset)
	for {
		varEntry, err := r.Next()
		if err != nil {
			break
		}
		if varEntry.Tag == 0 {
			// This tag marks the end of the function's DWARF entry's children.
			break
		}

		// Check this entry corresponds to a local variable or function parameter,
		// that it has the correct name, and that we can get its type and location.
		// If so, return them.
		if varEntry.Tag != dwarf.TagFormalParameter && varEntry.Tag != dwarf.TagVariable {
			continue
		}
		varName, ok := varEntry.Val(dwarf.AttrName).(string)
		if !ok {
			continue
		}
		if varName != name {
			continue
		}
		varTypeOffset, ok := varEntry.Val(dwarf.AttrType).(dwarf.Offset)
		if !ok {
			continue
		}
		varType, err := s.dwarfData.Type(varTypeOffset)
		if err != nil {
			continue
		}
		locationAttribute := varEntry.Val(dwarf.AttrLocation)
		if locationAttribute == nil {
			continue
		}
		locationDescription, ok := locationAttribute.([]uint8)
		if !ok {
			continue
		}
		frameOffset, err := evalLocation(locationDescription)
		if err != nil {
			continue
		}
		return framePointer + uint64(frameOffset), varType
	}

	return 0, nil
}

// findGlobalVar finds a global variable by name, and returns its address and
// DWARF type.  It returns a nil type on failure.
func (s *Server) findGlobalVar(name string) (uint64, dwarf.Type) {
	entry, err := s.dwarfData.LookupEntry(name)
	if err != nil {
		return 0, nil
	}
	loc, err := s.dwarfData.EntryLocation(entry)
	if err != nil {
		return 0, nil
	}
	ofs, err := s.dwarfData.EntryTypeOffset(entry)
	if err != nil {
		return 0, nil
	}
	typ, err := s.dwarfData.Type(ofs)
	if err != nil {
		return 0, nil
	}
	return loc, typ
}

// intFromInteger converts an untyped integer constant to an int32 or int64,
// depending on the int size of the debugged program.
// It returns an error on overflow, or if it can't determine the int size.
func (e *evaluator) intFromInteger(v untInt) (interface{}, error) {
	t, ok := e.getBaseType("int")
	if !ok {
		return nil, errors.New("couldn't get int size from DWARF info")
	}
	switch t.Common().ByteSize {
	case 4:
		if v.Cmp(bigIntMaxInt32) == +1 || v.Cmp(bigIntMinInt32) == -1 {
			return nil, errors.New("constant overflows int")
		}
		return int32(v.Int64()), nil
	case 8:
		if v.Cmp(bigIntMaxInt64) == +1 || v.Cmp(bigIntMinInt64) == -1 {
			return nil, errors.New("constant overflows int")
		}
		return v.Int64(), nil
	}
	return nil, errors.New("invalid int size in DWARF info")
}

// uint8Result constructs a result for a uint8 value.
func (e *evaluator) uint8Result(v uint8) result {
	t, ok := e.getBaseType("uint8")
	if !ok {
		e.err("couldn't construct uint8")
	}
	return result{t, uint8(v)}
}

// getBaseType returns the *dwarf.Type with a given name.
// TODO: cache this.
func (e *evaluator) getBaseType(name string) (dwarf.Type, bool) {
	entry, err := e.server.dwarfData.LookupEntry(name)
	if err != nil {
		return nil, false
	}
	t, err := e.server.dwarfData.Type(entry.Offset)
	if err != nil {
		return nil, false
	}
	return t, true
}

// resultFrom constructs a result corresponding to a value in the program with
// the given address and DWARF type.
// If getAddress is true, the result will be the operand of an address expression,
// so resultFrom returns a result containing a value of type addressableValue.
func (e *evaluator) resultFrom(a uint64, t dwarf.Type, getAddress bool) result {
	if a == 0 {
		return e.err("nil pointer dereference")
	}
	if getAddress {
		return result{t, addressableValue{a}}
	}
	v, err := e.server.value(t, a)
	if err != nil {
		return e.err(err.Error())
	}
	return result{t, v}
}

// convertUntyped converts x to be the same type as y, if x is untyped and the
// conversion is possible.
//
// An untyped bool can be converted to a boolean type.
// An untyped string can be converted to a string type.
// An untyped integer, rune, float or complex value can be converted to a
// numeric type, or to an untyped value later in that list.
//
// x is returned unchanged if none of these cases apply.
func convertUntyped(x, y result) result {
	switch a := x.v.(type) {
	case untInt:
		i := a.Int
		switch y.v.(type) {
		case int8:
			return result{y.d, int8(i.Int64())}
		case int16:
			return result{y.d, int16(i.Int64())}
		case int32:
			return result{y.d, int32(i.Int64())}
		case int64:
			return result{y.d, int64(i.Int64())}
		case uint8:
			return result{y.d, uint8(i.Uint64())}
		case uint16:
			return result{y.d, uint16(i.Uint64())}
		case uint32:
			return result{y.d, uint32(i.Uint64())}
		case uint64:
			return result{y.d, uint64(i.Uint64())}
		case float32:
			f, _ := new(big.Float).SetInt(i).Float32()
			return result{y.d, f}
		case float64:
			f, _ := new(big.Float).SetInt(i).Float64()
			return result{y.d, f}
		case complex64:
			f, _ := new(big.Float).SetInt(i).Float32()
			return result{y.d, complex(f, 0)}
		case complex128:
			f, _ := new(big.Float).SetInt(i).Float64()
			return result{y.d, complex(f, 0)}
		case untRune:
			return result{nil, untRune{i}}
		case untFloat:
			return result{nil, untFloat{new(big.Float).SetPrec(prec).SetInt(i)}}
		case untComplex:
			return result{nil, untComplex{new(big.Float).SetPrec(prec).SetInt(i), new(big.Float)}}
		}
	case untRune:
		i := a.Int
		switch y.v.(type) {
		case int8:
			return result{y.d, int8(i.Int64())}
		case int16:
			return result{y.d, int16(i.Int64())}
		case int32:
			return result{y.d, int32(i.Int64())}
		case int64:
			return result{y.d, int64(i.Int64())}
		case uint8:
			return result{y.d, uint8(i.Uint64())}
		case uint16:
			return result{y.d, uint16(i.Uint64())}
		case uint32:
			return result{y.d, uint32(i.Uint64())}
		case uint64:
			return result{y.d, uint64(i.Uint64())}
		case float32:
			f, _ := new(big.Float).SetInt(i).Float32()
			return result{y.d, f}
		case float64:
			f, _ := new(big.Float).SetInt(i).Float64()
			return result{y.d, f}
		case complex64:
			f, _ := new(big.Float).SetInt(i).Float32()
			return result{y.d, complex(f, 0)}
		case complex128:
			f, _ := new(big.Float).SetInt(i).Float64()
			return result{y.d, complex(f, 0)}
		case untRune:
			return result{nil, untRune{i}}
		case untFloat:
			return result{nil, untFloat{new(big.Float).SetPrec(prec).SetInt(i)}}
		case untComplex:
			return result{nil, untComplex{new(big.Float).SetPrec(prec).SetInt(i), new(big.Float)}}
		}
	case untFloat:
		if a.IsInt() {
			i, _ := a.Int(nil)
			switch y.v.(type) {
			case int8:
				return result{y.d, int8(i.Int64())}
			case int16:
				return result{y.d, int16(i.Int64())}
			case int32:
				return result{y.d, int32(i.Int64())}
			case int64:
				return result{y.d, int64(i.Int64())}
			case uint8:
				return result{y.d, uint8(i.Uint64())}
			case uint16:
				return result{y.d, uint16(i.Uint64())}
			case uint32:
				return result{y.d, uint32(i.Uint64())}
			case uint64:
				return result{y.d, uint64(i.Uint64())}
			}
		}
		switch y.v.(type) {
		case float32:
			f, _ := a.Float32()
			return result{y.d, float32(f)}
		case float64:
			f, _ := a.Float64()
			return result{y.d, float64(f)}
		case complex64:
			f, _ := a.Float32()
			return result{y.d, complex(f, 0)}
		case complex128:
			f, _ := a.Float64()
			return result{y.d, complex(f, 0)}
		case untComplex:
			return result{nil, untComplex{a.Float, new(big.Float)}}
		}
	case untComplex:
		if a.i.Sign() == 0 {
			// a is a real number.
			if a.r.IsInt() {
				// a is an integer.
				i, _ := a.r.Int(nil)
				switch y.v.(type) {
				case int8:
					return result{y.d, int8(i.Int64())}
				case int16:
					return result{y.d, int16(i.Int64())}
				case int32:
					return result{y.d, int32(i.Int64())}
				case int64:
					return result{y.d, int64(i.Int64())}
				case uint8:
					return result{y.d, uint8(i.Uint64())}
				case uint16:
					return result{y.d, uint16(i.Uint64())}
				case uint32:
					return result{y.d, uint32(i.Uint64())}
				case uint64:
					return result{y.d, uint64(i.Uint64())}
				}
			}
			switch y.v.(type) {
			case float32:
				f, _ := a.r.Float32()
				return result{y.d, float32(f)}
			case float64:
				f, _ := a.r.Float64()
				return result{y.d, float64(f)}
			}
		}
		switch y.v.(type) {
		case complex64:
			r, _ := a.r.Float32()
			i, _ := a.i.Float32()
			return result{y.d, complex(r, i)}
		case complex128:
			r, _ := a.r.Float64()
			i, _ := a.i.Float64()
			return result{y.d, complex(r, i)}
		}
	case bool:
		if x.d != nil {
			// x is a typed bool, not an untyped bool.
			break
		}
		switch y.v.(type) {
		case bool:
			return result{y.d, bool(a)}
		}
	case untString:
		switch y.v.(type) {
		case program.String:
			return result{y.d, program.String{Length: uint64(len(a)), String: string(a)}}
		}
	}
	return x
}

// followTypedefs returns the underlying type of t, removing any typedefs.
// If t leads to a cycle of typedefs, followTypedefs returns nil.
func followTypedefs(t dwarf.Type) dwarf.Type {
	// If t is a *dwarf.TypedefType, next returns t.Type, otherwise it returns t.
	// The bool returned is true when the argument was a typedef.
	next := func(t dwarf.Type) (dwarf.Type, bool) {
		tt, ok := t.(*dwarf.TypedefType)
		if !ok {
			return t, false
		}
		return tt.Type, true
	}
	// Advance two pointers, one at twice the speed, so we can detect if we get
	// stuck in a cycle.
	slow, fast := t, t
	for {
		var wasTypedef bool
		fast, wasTypedef = next(fast)
		if !wasTypedef {
			return fast
		}
		fast, wasTypedef = next(fast)
		if !wasTypedef {
			return fast
		}
		slow, _ = next(slow)
		if slow == fast {
			return nil
		}
	}
}
