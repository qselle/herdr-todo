"""Resolve project identity from Herdr's documented invocation context."""
import json
import os
from dataclasses import asdict, dataclass
from pathlib import Path
import subprocess


@dataclass(frozen=True)
class Context:
    project: str
    label: str
    workspace: str = ""
    pane: str = ""
    session: str = ""

    @classmethod
    def current(cls, project=None):
        carried = os.environ.get("HERDR_TODO_CONTEXT")
        raw = os.environ.get("HERDR_PLUGIN_CONTEXT_JSON", "{}")
        try:
            data = json.loads(carried or raw)
        except (ValueError, TypeError) as exc:
            raise ValueError("Herdr supplied invalid workspace context") from exc
        if not isinstance(data, dict):
            raise ValueError("Herdr workspace context must be a JSON object")
        if carried:
            if project:
                path = str(Path(project).expanduser().resolve())
                return cls(path, Path(path).name or path)
            return cls(**data)
        cwd = project or data.get("workspace_cwd") or data.get("focused_pane_cwd")
        if not cwd:
            cwd = os.environ.get("HERDR_ACTIVE_PANE_CWD")
        # Plugin commands start in their source directory. Never assign tasks
        # to that directory just because Herdr omitted the invocation context.
        if not cwd and os.environ.get("HERDR_PLUGIN_ROOT"):
            raise ValueError("No project directory in Herdr context; use --project PATH")
        path = str(Path(cwd or Path.cwd()).expanduser().resolve())
        return cls(
            path,
            (data.get("workspace_label") if not project else None) or Path(path).name or path,
            data.get("workspace_id") or os.environ.get("HERDR_WORKSPACE_ID", ""),
            data.get("focused_pane_id") or os.environ.get("HERDR_PANE_ID", ""),
            os.environ.get("HERDR_SOCKET_PATH", ""),
        )

    def json(self):
        return json.dumps(asdict(self), ensure_ascii=False)


def open_popup(context):
    command = [os.environ.get("HERDR_BIN_PATH", "herdr"), "plugin", "pane", "open",
               "--plugin", "herdr-todo", "--entrypoint", "tasks",
               "--env", "HERDR_TODO_CONTEXT=" + context.json()]
    # Popup placement always targets Herdr's active pane and rejects explicit
    # workspace/pane targets. Carry the originating project separately so a
    # focus change cannot silently reassign newly captured tasks.
    if os.environ.get("HERDR_TODO_DB"):
        command += ["--env", "HERDR_TODO_DB=" + os.environ["HERDR_TODO_DB"]]
    result = subprocess.run(command, capture_output=True, text=True, timeout=15)
    if result.returncode:
        raise RuntimeError(result.stderr.strip() or result.stdout.strip() or "Could not open tasks")
    return result.stdout
