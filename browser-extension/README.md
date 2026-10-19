# Annulo browser extension

Lets [Annulo](https://github.com/annulo/annulo) on your computer work in your own Chrome, signed in as you already are,
on the sites you allow. Local functions use it with `ctx.browser.open({ profile: '@user' })`.

## How it works

Annulo registers itself as a native messaging host (`com.annulo.browser`). The extension connects to it with
`chrome.runtime.connectNative`, and Annulo drives tabs the extension opens through `chrome.debugger`.

`chrome.debugger` is not limited by host permissions, so the limits live in `extension/background.js`:

- only tabs the extension opened for Annulo are ever attached;
- a tab may only be on a site you allowed (optional host permissions, asked per site the first time Annulo needs it);
  leaving the allowed sites detaches the debugger and fails every later command;
- only allow-listed DevTools commands are forwarded; cookie, storage, target and browser-wide commands never are.

You can see and remove the allowed sites from the extension's toolbar menu.

## Development

Load `extension/` as an unpacked extension (chrome://extensions → Developer mode → Load unpacked). The `key` in
`manifest.json` pins the development ID `emhbcpdadcannpcngbkjiaccpckjeend`, which Annulo's native host manifest allows.
Annulo must be running; it writes the native host manifest every time it starts.

`scripts/pack.sh` builds `dist/annulo-extension-<version>.zip` for the Chrome Web Store (without the `key`).

