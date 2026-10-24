# Bitmasks

A bitmask keeps a small set of numbers as the bits of one integer: bit n is 1
when n is in the set. A `uint64` holds any set of the numbers 0 to 63.

```go
var set uint64
set |= 1 << 7          // add 7: shift a 1 left 7 places, then OR it in
in := set&(1<<7) != 0  // is 7 in? AND keeps only bit 7
set &^= 1 << 7         // remove 7: AND NOT clears the bit
```

| Operator | Name | Use here |
| --- | --- | --- |
| `<<` | shift left | `1 << n` is a number with only bit n set |
| `\|` | OR | add a value to the set |
| `&` | AND | test whether a value is in |
| `&^` | AND NOT | take a value out; Go's own operator, `a & ^b` in other languages |

`math/bits` counts and finds bits: `bits.OnesCount64(set)` says how many values
the set holds, and `bits.TrailingZeros64(set)` gives the lowest one.

In Python the same set would be a `set` of ints. A bitmask is smaller and
faster, and two masks compare with `==`, which is handy when the sets are tiny
and fixed in size, as cron's fields are.

Meru uses one per cron field in [jobs](../jobs.md).
