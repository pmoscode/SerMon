# sudo failures

sudo calls with a wrong password and calls of users not allowed to use sudo, in the range.

## Assessment

- Single typos of the password are normal.
- Many failures, or attempts of users without sudo rights, may mean someone with a taken-over account is looking for more rights.

## Next steps

1. Check in the table which user it was and which target they wanted.
2. Ask the person whether the attempts were theirs; otherwise lock the account.
