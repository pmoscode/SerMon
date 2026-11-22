# Tags

Tags are free `key=value` pairs on a node, such as `env=prod` or `role=db`. They sort nodes across groups: a group decides the rights, tags never do.

## What for

- **Filters:** The fleet overview filters by a tag; the search finds tags.
- **Rules:** A rule's selector can ask for tags (`selector: { tags: { env: prod } }`); it then applies only to nodes that carry all of them.
- **Notifications:** Notification policies and silences can match the node's tags, e.g. "page only for `env=prod` at night" or "mute `role=backup` during the migration".

## Rules for tags

- Keys: lowercase letters, digits, `_` and `-`, beginning with a letter, at most 32 characters.
- Values: letters, digits and `_ . : / -`, beginning with a letter or digit, at most 63 characters.
- At most 16 tags per node, each key once.

Admins of the node's group set tags, with a fresh password confirmation; every change is in the audit log with the old and the new state. Removing a tag ends the alerts of rules that ask for it.
