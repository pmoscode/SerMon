//! Cutting a message to its bound without leaving the start of a secret (M5-33): the redaction
//! runs after the cut and sees only what is kept, so a token the cut goes through would leave
//! its first part unrecognised (`AKIA…` without the rest, half a key).

/// How far back a cut drops the token it went through.
pub const TOKEN_BACK: usize = 1024;

/// `text`, at most `max` bytes, cut at a character boundary. If the cut goes through a token
/// (no whitespace on either side of it), the token's kept part goes too: back to the last
/// whitespace, at most [`TOKEN_BACK`] bytes.
#[must_use]
pub fn message(text: &str, max: usize) -> &str {
    if text.len() <= max {
        return text;
    }
    let mut end = max;
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    let inside = |c: Option<char>| c.is_some_and(|c| !c.is_whitespace());
    if !(inside(text[..end].chars().next_back()) && inside(text[end..].chars().next())) {
        return &text[..end];
    }
    let mut floor = end.saturating_sub(TOKEN_BACK);
    while !text.is_char_boundary(floor) {
        floor += 1;
    }
    match text[floor..end].rfind(char::is_whitespace) {
        Some(at) => &text[..floor + at],
        None => &text[..floor],
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_token_the_cut_goes_through_goes_entirely() {
        let secret = "AKIAIOSFODNN7EXAMPLE";
        let text = format!("{} key={secret} done", "word ".repeat(10));
        let at = text.find(secret).unwrap() + 6;
        assert_eq!(message(&text, at), "word ".repeat(10));
        // At whitespace: nothing more goes.
        let space = text.find(" key=").unwrap();
        assert_eq!(message(&text, space), &text[..space]);
        assert_eq!(message(&text, space + 1), &text[..=space]);
        assert_eq!(message(&text, text.len()), text);
        // No whitespace within reach: TOKEN_BACK bytes go.
        let blob = "x".repeat(5000);
        assert_eq!(message(&blob, 4000).len(), 4000 - TOKEN_BACK);
        // Character boundaries hold.
        let umlauts = "ä".repeat(3000);
        let cut = message(&umlauts, 4001);
        assert!(cut.len() <= 4001 - TOKEN_BACK && cut.chars().all(|c| c == 'ä'));
        assert_eq!(message("ab", 1), "");
        assert_eq!(message("abc def", 3), "abc", "the token ends at the cut");
        assert_eq!(
            message("abc def", 4),
            "abc ",
            "the next one starts after it"
        );
    }
}
