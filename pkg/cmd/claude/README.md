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



### Workflow Diagram

The following diagram illustrates the execution flow of the `depot claude` command, including the main process and the background task for continuous saving.

**Note:** This Mermaid diagram might not render directly on GitHub due to limitations in their renderer. If the diagram does not display correctly, you can view it using a local Markdown viewer with Mermaid support (e.g., VS Code with the Mermaid extension) or an online Mermaid live editor (e.g., [Mermaid Live Editor](https://mermaid.live/)).

```mermaid
graph TD
    subgraph "Main Process"
        A[User runs `depot claude [args]`] --> B(NewCmdClaude Entry Point);
        B --> C{Parse CLI Arguments};
        C --> D[Verify Auth with Depot API];
        D --> E{Is `--resume` flag used?};

        E -- Yes --> F(resumeSession);
        F --> G[API Call: DownloadClaudeSession];
        G --> H[Save session file locally];
        H --> I[Start `claude --resume` subprocess];

        E -- No --> J[Start `claude` subprocess];

        subgraph "Claude Execution"
            I --> K;
            J --> K;
            K(Claude process runs...);
        end

        subgraph "Final Save on Exit"
            L[Wait for Claude process to exit] --> M[Find session file path];
            M --> N(saveSession - Final);
            N --> O[API Call: UploadClaudeSession];
            O --> P[Done];
        end

        K --> L;
    end

    subgraph "Background Goroutine (Continuous Save)"
        direction LR
        Q(continuouslySaveSessionFile);
        R{Watch session directory for changes};
        S{Session file created or changed?};
        T(saveSession - Continuous);
        U[API Call: UploadClaudeSession];

        Q --> R;
        R --> S;
        S -- Yes --> T;
        T --> U;
        U --> R;
        S -- No --> R;
    end

    %% Link main process to background process
    I ==> Q;
    J ==> Q;

    %% Styling
    style F fill:#cde4ff,stroke:#333,stroke-width:2px
    style N fill:#cde4ff,stroke:#333,stroke-width:2px
    style T fill:#d4edda,stroke:#333,stroke-width:2px
    style G fill:#f8d7da,stroke:#333,stroke-width:2px
    style O fill:#f8d7da,stroke:#333,stroke-width:2px
    style U fill:#f8d7da,stroke:#333,stroke-width:2px
```

## Key Functions

-   `NewCmdClaude()`: Initializes the cobra command, defines flags, and contains the main run loop.
-   `resumeSession()`: Handles the logic for downloading a session from Depot.
-   `saveSession()`: Handles the logic for uploading a session to Depot. It is called both by the continuous save mechanism and for the final save.
-   `continuouslySaveSessionFile()`: Runs in a background goroutine to monitor the session file for changes and trigger uploads.
-   `findLatestSessionFile()`: A helper function to locate the most recently modified session file in the project's session directory, used when a session ID is not explicitly provided.
-   `convertPathToProjectName()`: A utility to convert a file system path into the project name format that the `claude` CLI expects.
