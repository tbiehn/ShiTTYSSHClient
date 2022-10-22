# ShiTTYSSHClient

The companion tool to [ShiTTYSSH](https://github.com/tbiehn/ShiTTYSSH) - wraps any command that can receive a (rev/bind) shell and dishes stdin/stdout and optionally stderr over a TCP socket.

Just one client at a time, please.

## Why?

So you can move your shell around to different TTYs and processes.

## And?

Thats what `ShiTTYSSH` needs. Maybe you want to write a bunch of EXPECT(1) scripts to manage shells and detach/reattach at will.