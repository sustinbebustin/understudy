package fault

// DecodeMembers and MemberError expose the members reader to the external
// tests, which can import generated packages; the package's own tests
// cannot, since those import the kernel, which imports fault.
var DecodeMembers = decodeMembers

type MemberError = memberError
