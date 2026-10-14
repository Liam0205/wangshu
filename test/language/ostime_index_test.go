package language_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/Liam0205/wangshu/test/testutil"
)

// TestOSTimeIndex pins the lua_getfield semantics of PUC Lua 5.1.5's
// getfield/getboolfield, including lookup order and errors from __index.
func TestOSTimeIndex(t *testing.T) {
	origLocal := time.Local
	t.Setenv("TZ", "UTC")
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = origLocal })

	for _, tc := range []struct {
		name, src, want string
	}{
		{"function-index", `
local fields = {year=2000, month=1, day=2, hour=12, min=0, sec=0, isdst=false}
local seen = {}
local d = setmetatable({}, {__index=function(self, k)
  assert(type(self) == "table")
  seen[#seen+1] = k
  return fields[k]
end})
local epoch = os.time(d)
return tostring(epoch) .. ":" .. table.concat(seen, ",")
`, "946814400:sec,min,hour,day,month,year,isdst"},
		{"table-index-chain", `
local fields = {year=2000, month=1, day=2, isdst=false}
local middle = setmetatable({}, {__index=fields})
return tostring(os.time(setmetatable({}, {__index=middle})))
`, "946814400"},
		{"optional-defaults", `
local seen = {}
local d = setmetatable({year=2000, month=1, day=2}, {__index=function(_, k)
  seen[#seen+1] = k
  return nil
end})
local epoch = os.time(d)
return tostring(epoch) .. ":" .. table.concat(seen, ",")
`, "946814400:sec,min,hour,isdst"},
		{"present-fields-bypass-index", `
local d = setmetatable({year=2000, month=1, day=2, hour=12, min=0, sec=0, isdst=false},
  {__index=function() error("unexpected lookup", 0) end})
return tostring(os.time(d))
`, "946814400"},
		{"missing-required-field", `
local seen = {}
local d = setmetatable({}, {__index=function(_, k)
  seen[#seen+1] = k
end})
local ok, err = pcall(os.time, d)
assert(not ok and string.find(err, "field 'day' missing in date table", 1, true))
return table.concat(seen, ",")
`, "sec,min,hour,day"},
		{"lookup-mutation", `
local d = setmetatable({}, {__index=function(self, k)
  if k == "sec" then
    rawset(self, "year", 2000)
    rawset(self, "month", 1)
    rawset(self, "day", 2)
  end
end})
return tostring(os.time(d))
`, "946814400"},
		{"fractional-fields", `
local fields = {year=2000.9, month=1.9, day=2.9, hour=12.9, min=0.9, sec=0.9}
return tostring(os.time(setmetatable({}, {__index=fields})))
`, "946814400"},
		{"index-cycle", `
local d = {}
setmetatable(d, {__index=d})
local ok, err = pcall(os.time, d)
assert(not ok)
return err
`, "loop in gettable"},
		{"collection-in-handler", `
local fields = {year=2000, month=1, day=2}
local d = setmetatable({}, {__index=function(_, k)
  collectgarbage("collect")
  return fields[k]
end})
return tostring(os.time(d))
`, "946814400"},
		{"reentrant-handler", `
local fields = {year=2000, month=1, day=2}
local d = setmetatable({}, {__index=function(_, k)
  assert(os.time(fields) == 946814400)
  return fields[k]
end})
return tostring(os.time(d))
`, "946814400"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}

	for _, field := range []string{"sec", "min", "hour", "day", "month", "year", "isdst"} {
		t.Run("error-from-"+field, func(t *testing.T) {
			src := fmt.Sprintf(`
local fields = {year=2000, month=1, day=2, hour=12, min=0, sec=0, isdst=false}
fields[%q] = nil
local d = setmetatable(fields, {__index=function(_, k) error("hook:" .. k, 0) end})
local ok, err = pcall(os.time, d)
assert(not ok)
return err
`, field)
			if got := testutil.RunOne(t, src).Str(); got != "hook:"+field {
				t.Errorf("got %q, want hook:%s", got, field)
			}
		})
	}
}
