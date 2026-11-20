//! Where the journal source resumes (ADR 0013): the consumer commits the cursor of an entry once
//! its results are stored (WAL); the source writes the committed cursor to a file now and then
//! and reads it at the start. After a crash at most the entries since the last write come again.

use std::fs;
use std::io::{self, Write};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, PoisonError};

use super::entry::valid_cursor;

/// The cursor of the last entry whose results are stored, shared between consumer and source.
#[derive(Debug, Clone, Default)]
pub struct Checkpoint {
    inner: Arc<Mutex<State>>,
}

#[derive(Debug, Default)]
struct State {
    committed: Option<String>,
    dirty: bool,
}

impl Checkpoint {
    /// A checkpoint at `committed`.
    #[must_use]
    pub fn new(committed: Option<String>) -> Self {
        Self {
            inner: Arc::new(Mutex::new(State {
                committed,
                dirty: false,
            })),
        }
    }

    /// Marks the entry at `cursor` (and every one before it) as stored.
    pub fn commit(&self, cursor: &str) {
        let mut state = self.lock();
        if state.committed.as_deref() != Some(cursor) {
            state.committed = Some(cursor.to_owned());
            state.dirty = true;
        }
    }

    /// The committed cursor.
    #[must_use]
    pub fn committed(&self) -> Option<String> {
        self.lock().committed.clone()
    }

    /// The committed cursor if it changed since the last call.
    pub(crate) fn take_changed(&self) -> Option<String> {
        let mut state = self.lock();
        if !state.dirty {
            return None;
        }
        state.dirty = false;
        state.committed.clone()
    }

    /// Marks the cursor as changed again after a failed write.
    pub(crate) fn mark_changed(&self) {
        self.lock().dirty = true;
    }

    fn lock(&self) -> std::sync::MutexGuard<'_, State> {
        self.inner.lock().unwrap_or_else(PoisonError::into_inner)
    }
}

/// The committed cursor on disk.
#[derive(Debug, Clone)]
pub struct CursorFile {
    path: PathBuf,
}

impl CursorFile {
    /// The file at `path`.
    #[must_use]
    pub fn new(path: impl Into<PathBuf>) -> Self {
        Self { path: path.into() }
    }

    /// The file's path.
    #[must_use]
    pub fn path(&self) -> &Path {
        &self.path
    }

    /// The stored cursor; `None` if there is none or it is not a valid cursor (the source then
    /// starts at the end of the journal).
    ///
    /// # Errors
    ///
    /// Reading fails for another reason than a missing file.
    pub fn load(&self) -> io::Result<Option<String>> {
        match fs::read_to_string(&self.path) {
            Ok(text) => Ok(Some(text.trim().to_owned()).filter(|c| valid_cursor(c))),
            Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(None),
            Err(error) => Err(error),
        }
    }

    /// Replaces the stored cursor atomically (temporary file, fsync, rename, fsync of the directory).
    ///
    /// # Errors
    ///
    /// Writing fails; the previous cursor then stays in place.
    pub fn store(&self, cursor: &str) -> io::Result<()> {
        if !valid_cursor(cursor) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "invalid cursor",
            ));
        }
        let directory = self.path.parent().unwrap_or(Path::new("."));
        let name = self
            .path
            .file_name()
            .map_or_else(|| "cursor".into(), |n| n.to_string_lossy().into_owned());
        let temporary = directory.join(format!(".{name}.new"));
        let result = write_private(&temporary, cursor.as_bytes())
            .and_then(|()| fs::rename(&temporary, &self.path))
            .and_then(|()| fs::File::open(directory)?.sync_all());
        if result.is_err() {
            let _ = fs::remove_file(&temporary);
        }
        result
    }
}

fn write_private(path: &Path, content: &[u8]) -> io::Result<()> {
    let mut options = fs::OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    std::os::unix::fs::OpenOptionsExt::mode(&mut options, 0o600);
    let mut file = options.open(path)?;
    file.write_all(content)?;
    file.write_all(b"\n")?;
    file.sync_all()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn stores_and_loads_atomically() {
        let dir = tempfile::tempdir().unwrap();
        let file = CursorFile::new(dir.path().join("journal.cursor"));
        assert_eq!(file.load().unwrap(), None, "no file yet");
        file.store("s=a;i=1").unwrap();
        file.store("s=a;i=2").unwrap();
        assert_eq!(file.load().unwrap().as_deref(), Some("s=a;i=2"));
        let names: Vec<_> = fs::read_dir(dir.path())
            .unwrap()
            .map(|e| e.unwrap().file_name())
            .collect();
        assert_eq!(names, ["journal.cursor"], "no temporary file left");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = fs::metadata(file.path()).unwrap().permissions().mode();
            assert_eq!(mode & 0o777, 0o600);
        }
    }

    #[test]
    fn rejects_invalid_cursors() {
        let dir = tempfile::tempdir().unwrap();
        let file = CursorFile::new(dir.path().join("journal.cursor"));
        assert!(file.store("two words").is_err());
        fs::write(file.path(), "--lines=1000 --since=yesterday").unwrap();
        assert_eq!(file.load().unwrap(), None, "a broken file is no cursor");
        fs::write(file.path(), "x".repeat(10_000)).unwrap();
        assert_eq!(file.load().unwrap(), None);
    }

    #[test]
    fn checkpoint_reports_changes_once() {
        let checkpoint = Checkpoint::new(Some("s=a;i=1".into()));
        assert_eq!(
            checkpoint.take_changed(),
            None,
            "the loaded cursor is no change"
        );
        checkpoint.commit("s=a;i=2");
        checkpoint.commit("s=a;i=3");
        assert_eq!(checkpoint.take_changed().as_deref(), Some("s=a;i=3"));
        assert_eq!(checkpoint.take_changed(), None);
        checkpoint.commit("s=a;i=3");
        assert_eq!(checkpoint.take_changed(), None, "same cursor again");
        checkpoint.mark_changed();
        assert_eq!(checkpoint.take_changed().as_deref(), Some("s=a;i=3"));
        assert_eq!(checkpoint.committed().as_deref(), Some("s=a;i=3"));
    }
}
