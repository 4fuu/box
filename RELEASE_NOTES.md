## Portals

- Renaming an online computer no longer interrupts its portals. The
  server reuses connections only when the computer and port still match
  the current route.
- If the upstream connection fails while forwarding a request, box does
  not resend it. The client decides whether to try again. Connection
  reuse and the short wait for a tunnel before forwarding still work.
