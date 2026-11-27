# Security

Set up two-factor authentication for your account here: after the password, SerMon also asks for a code from an authenticator app.

## Setting it up

1. Choose “Set up” and confirm your password if asked.
2. Scan the QR code with the app (e.g. Aegis, 2FAS, Google Authenticator), or enter the key by hand.
3. Enter the 6-digit code shown.
4. Keep the **recovery codes** safe. They are shown only once; each replaces the app a single time.

## Passkeys

A passkey signs you in without a password: your device (fingerprint, face, PIN) or a security key confirms you, and it works only on this SerMon instance – a fake page can't grab it. Passkeys need a domain name: if you reach SerMon by an IP address or a name without a dot, the section is missing.

- **Add:** enter a name (e.g. “laptop”), choose “Add a passkey” and follow the browser. SerMon asks for a confirmation first. Up to 10 passkeys per account.
- **Sign in:** “Sign in with a passkey” on the sign-in page, or the browser offers it in the e-mail field.
- A passkey is also your **second factor**: after the password instead of the code and for confirming sensitive actions. If your organization requires two factors, a passkey is enough.
- **Synced** means: your password manager or device account backs up the passkey; it is then available on your other devices too.
- Deleting needs a confirmation. If you delete your last second factor, an organization that requires one asks for the setup again.
- Deleting signs you out on all other devices, so a lost passkey is useless for sessions already open too. The same holds for unlinking an SSO identity.

## Sign-in over SSO

If the instance has an identity provider, link your account with it here (“Link with …”, after a fresh confirmation). Then you sign in over the provider on the sign-in page. SerMon recognizes you by your identity at the provider, not by your e-mail.

- **Unlink** works only while another way in remains, usually your password. Accounts SerMon created at the first sign-in over the provider have no password.
- Confirmations for sensitive actions also work over the provider: it asks for a fresh sign-in, then you run the action again.
- If your organization requires two-factor authentication, the provider's second factor counts if the operator set it up so; otherwise SerMon asks for your own code after the sign-in.

## Good to know

- Every code from the app works only once. If a code doesn't match, check the device's clock.
- For actions such as changing rights, SerMon asks for a code again; the confirmation lasts 15 minutes ([confirmation](help:concepts/step-up)).
- If your organization requires two-factor authentication, its pages are available only after the setup.
- Five wrong codes lock the account for a while, just like wrong passwords.
