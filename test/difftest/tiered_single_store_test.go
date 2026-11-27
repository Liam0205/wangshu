//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

package difftest

import "testing"

// tieredSingleStoreCases are stores made by a function whose whole body is one SETTABLE with a
// constant key, to a field that is absent at the time of the store. P4 compiles such a function
// into a dedicated template that writes into the table's slot directly; a store to an absent
// field has to consult __newindex instead, and a nil store is a deletion.
//
// The cases have the two-chunk form of tieredClearCases, for the same reason: the template is only
// emitted from an inline cache the interpreter has filled.
var tieredSingleStoreCases = []struct{ name, warm, run string }{
	// The function deletes the field itself and then stores into it again.
	{"hash_field_set_to_nil_then_stored", `
log347 = 0
S347 = setmetatable({x = 0, y = 0}, {__newindex = function(t, k, v) log347 = log347 + 1 rawset(t, k, v) end})
function setter347(t, v) t["x"] = v end
for i = 1, 40 do setter347(S347, i) end`, `
for i = 1, 5 do setter347(S347, i) end
for i = 1, 10 do
  setter347(S347, nil)
  setter347(S347, i)
end
return log347, S347.x`},
	// The same in the array part.
	{"array_slot_set_to_nil_then_stored", `
log347 = 0
S347 = setmetatable({1, 2, 3}, {__newindex = function(t, k, v) log347 = log347 + 1 rawset(t, k, v) end})
function setter347(t, v) t[2] = v end
for i = 1, 40 do setter347(S347, i) end`, `
for i = 1, 5 do setter347(S347, i) end
for i = 1, 10 do
  setter347(S347, nil)
  setter347(S347, i)
end
return log347, S347[2], #S347`},
	// The function is compiled from stores to one table and then called on other tables, built by
	// the same constructor, in which the field has been deleted. Such a table has the same
	// generation as the first and still holds the key in the same node.
	{"hash_field_deleted_in_another_table", `
log347 = 0
mt347 = {__newindex = function(t, k, v) log347 = log347 + 1 rawset(t, k, v) end}
function new347() return setmetatable({x = 0, y = 0, z = 0}, mt347) end
A347 = new347()
A347.z = nil
function setter347(t, v) t["x"] = v end
for i = 1, 40 do setter347(A347, i) end`, `
for i = 1, 5 do setter347(A347, i) end
local total = 0
for i = 1, 10 do
  local B = new347()
  B.x = nil
  setter347(B, i)
  total = total + B.x
end
return log347, total, A347.x`},
	// The same in the array part: the other tables are built with a hole where the first has a value.
	{"array_slot_empty_in_another_table", `
log347 = 0
mt347 = {__newindex = function(t, k, v) log347 = log347 + 1 rawset(t, k, v) end}
A347 = setmetatable({1, 2, 3}, mt347)
function setter347(t, v) t[2] = v end
for i = 1, 40 do setter347(A347, i) end`, `
for i = 1, 5 do setter347(A347, i) end
local total = 0
for i = 1, 10 do
  local B = setmetatable({1, nil, 3}, mt347)
  setter347(B, i)
  total = total + B[2]
end
return log347, total, A347[2]`},
	// Without a metatable the same stores only have to arrive.
	{"absent_fields_without_metatable", `
A347 = {x = 0, y = 0}
L347 = {1, 2, 3}
function setx347(t, v) t["x"] = v end
function set2347(t, v) t[2] = v end
for i = 1, 40 do setx347(A347, i) set2347(L347, i) end`, `
for i = 1, 5 do setx347(A347, i) set2347(L347, i) end
local out = {}
for i = 1, 6 do
  setx347(A347, nil) set2347(L347, nil)
  out[#out + 1] = tostring(A347.x) .. tostring(L347[2]) .. #L347
  setx347(A347, i) set2347(L347, i)
  out[#out + 1] = tostring(A347.x) .. tostring(L347[2]) .. #L347
  local B, M = {x = 0, y = 0}, {1, nil, 3}
  B.x = nil
  setx347(B, i) set2347(M, i)
  out[#out + 1] = tostring(B.x) .. tostring(M[2])
end
return table.concat(out, " ")`},
	// The function is compiled from stores to one table and then called on an empty table with
	// the same metatable: the slot it was compiled for lies outside that table, and the store is
	// to an absent field.
	{"array_slot_outside_another_table", `
log347 = 0
mt347 = {__newindex = function(t, k, v) log347 = log347 + 1 rawset(t, k, v) end}
A347 = setmetatable({1, 2, 3}, mt347)
function setter347(t, v) t[2] = v end
for i = 1, 40 do setter347(A347, i) end`, `
for i = 1, 5 do setter347(A347, i) end
local total = 0
for i = 1, 10 do
  local B = setmetatable({}, mt347)
  local neighbour = {11, 22, 33}
  setter347(B, i)
  total = total + rawget(B, 2) + neighbour[2]
end
return log347, total, A347[2]`},
}

// TestTiered_SingleStoreTemplate runs each case the four ways TestTiered_ClearWhileTraversing does
// and requires them to agree with each other and with lua5.1.
func TestTiered_SingleStoreTemplate(t *testing.T) {
	oracle := findOracle()
	for _, c := range tieredSingleStoreCases {
		t.Run(c.name, func(t *testing.T) { checkWarmThenForced(t, oracle, c.warm, c.run) })
	}
}
