package plist

func unsignedGetBase(s string) (string, int) {
	if len(s) > 1 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		return s[2:], 16
	}
	return s, 10
}
