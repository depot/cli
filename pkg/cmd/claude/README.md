# Claude Command Package

This package provides the implementation for the `depot claude` command, which acts as a wrapper around the standard `claude` CLI. It enhances the `claude` CLI by adding automatic session persistence, allowing users to save, resume, and share their Claude sessions through Depot.

## How it Works

The command intercepts a few specific flags (`--session-id`, `--resume`, `--org`, `--token`, `--output`) and passes all other arguments directly to the `claude` executable.

The core logic is as follows:

1.  **Argument Parsing**: Manually parses arguments to distinguish between `depot` flags and `claude` flags.
2.  **Authentication**: Verifies the user's Depot API token before proceeding.
3.  **Session Resuming**: If the `--resume` flag is used, it downloads the specified session file from Depot's servers and places it in the local Claude session directory (`~/.claude/projects`).
4.  **Subprocess Execution**: It starts the `claude` CLI as a subprocess, either resuming a session or starting a new one.
5.  **Continuous Saving**: A background goroutine uses a file watcher (`fsnotify`) to monitor the session file for changes. Whenever the file is written to, the updated session is immediately uploaded to Depot. This enables live viewing of the session.
6.  **Final Save**: Once the `claude` subprocess exits, a final save operation ensures the complete session is uploaded to Depot.



## Key Functions

-   `NewCmdClaude()`: Initializes the cobra command, defines flags, and contains the main run loop.
-   `resumeSession()`: Handles the logic for downloading a session from Depot.
-   `saveSession()`: Handles the logic for uploading a session to Depot. It is called both by the continuous save mechanism and for the final save.
-   `continuouslySaveSessionFile()`: Runs in a background goroutine to monitor the session file for changes and trigger uploads.
-   `findLatestSessionFile()`: A helper function to locate the most recently modified session file in the project's session directory, used when a session ID is not explicitly provided.
-   `convertPathToProjectName()`: A utility to convert a file system path into the project name format that the `claude` CLI expects.
