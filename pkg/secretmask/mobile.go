package secretmask

// cnMobilePrefixes is the set of 1[3-9]x three-digit heads the MIIT has
// allocated to mainland mobile services. The table is evidence of allocation,
// not of identity: it is public, it grows as new segments open, and millions of
// unrelated eleven-digit values start with one of these heads. The rule treats
// it as a CandidateCheck for exactly that reason - it narrows what may be
// reported, and only the surrounding text decides whether a value is rewritten.
//
// 146 and 148 are IoT segments and 192 belongs to China Broadnet; for the shape
// of a number they are ordinary mobile heads. 174 is satellite numbering
// (1740/1741/1749) and is deliberately absent.
var cnMobilePrefixes = [...]int{
	130, 131, 132, 133, 134, 135, 136, 137, 138, 139,
	145, 146, 147, 148, 149,
	150, 151, 152, 153, 155, 156, 157, 158, 159,
	162, 165, 166, 167,
	170, 171, 172, 173, 175, 176, 177, 178,
	180, 181, 182, 183, 184, 185, 186, 187, 188, 189,
	190, 191, 192, 193, 195, 196, 197, 198, 199,
}

// cnMobilePrefixSet is the lookup form of cnMobilePrefixes, indexed by the
// three-digit head as a number.
var cnMobilePrefixSet = func() [1000]bool {
	var set [1000]bool
	for _, p := range cnMobilePrefixes {
		set[p] = true
	}
	return set
}()

// candidateCNMobilePrefix is the CandidateCheck name used by RuleCNMobile.
const candidateCNMobilePrefix = "cn_mobile_prefix"

// runCandidateCheck dispatches to the named non-proving filter. An unrecognised
// name fails closed so that a rule cannot silently lose its filter;
// TestCandidateChecksKnown keeps the table and this switch in step.
func runCandidateCheck(name string, tok []byte) bool {
	switch name {
	case "":
		return true
	case candidateCNMobilePrefix:
		return validCNMobilePrefix(tok)
	case candidateURLShape:
		return validURLShape(tok)
	case candidateLocalPathShape:
		return validLocalPathShape(tok)
	default:
		return false
	}
}

// validCNMobilePrefix reports whether tok is eleven digits with an allocated
// 1[3-9]x head. It says nothing about the eight subscriber digits, and a pass
// is deliberately not treated as proof of anything.
func validCNMobilePrefix(tok []byte) bool {
	if len(tok) != 11 || tok[0] != '1' || tok[1] < '3' || tok[1] > '9' {
		return false
	}
	for _, c := range tok {
		if c < '0' || c > '9' {
			return false
		}
	}
	head := int(tok[0]-'0')*100 + int(tok[1]-'0')*10 + int(tok[2]-'0')
	return cnMobilePrefixSet[head]
}

// cnMobileHead returns the three-digit head of tok when it is an allocated one,
// and otherwise another allocated head chosen from the keystream. It is used
// when rendering a stand-in, so even a direct render of a value the candidate
// check would have rejected still produces a number a shape check admits.
func cnMobileHead(tok, ks []byte) int {
	if validCNMobilePrefix(tok) {
		return int(tok[0]-'0')*100 + int(tok[1]-'0')*10 + int(tok[2]-'0')
	}
	return cnMobilePrefixes[int(ks[0])%len(cnMobilePrefixes)]
}
