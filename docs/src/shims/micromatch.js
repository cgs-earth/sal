function escapeRegex(value) {
	return value.replace(/[|\\{}()[\]^$+?.]/g, '\\$&');
}

function globToRegex(pattern) {
	let source = '';
	for (let i = 0; i < pattern.length; i++) {
		const char = pattern[i];
		const next = pattern[i + 1];
		if (char === '*') {
			if (next === '*') {
				source += '.*';
				i++;
			} else {
				source += '[^/]*';
			}
			continue;
		}
		source += escapeRegex(char);
	}
	return new RegExp(`^${source}$`);
}

function isMatch(value, pattern) {
	const patterns = Array.isArray(pattern) ? pattern : [pattern];
	for (const item of patterns) {
		if (globToRegex(item).test(value)) {
			return true;
		}
	}
	return false;
}

export default { isMatch };
export { isMatch };
