// Error codes are part of the daemon protocol: no_tab, tab_closed, stale_ref,
// invalid_args, timeout, debugger, internal.
export class ActionError extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
  }
}
