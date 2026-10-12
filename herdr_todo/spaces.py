"""Optional links to existing workspaces in the current Herdr session."""
import json
import os
import subprocess


def space_link(value):
    if value is None:
        return None
    if not isinstance(value, dict) or any(not isinstance(value.get(key), str) or not value[key]
                                          for key in ("workspace_id", "label", "session")):
        raise ValueError("Choose a space from Herdr, or leave it unattached")
    return {key: value[key] for key in ("workspace_id", "label", "session")}


class Spaces:
    def __init__(self, context):
        self.context = context
        self.items = {}

    def command(self, *args):
        if os.environ.get("HERDR_ENV") != "1" or not self.context.session:
            raise ValueError("Open Tasks inside Herdr to choose a space")
        env = dict(os.environ, HERDR_SOCKET_PATH=self.context.session)
        try:
            result = subprocess.run([os.environ.get("HERDR_BIN_PATH", "herdr"), "workspace", *args],
                                    env=env, capture_output=True, text=True, timeout=3)
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise ValueError("Couldn't reach Herdr. Try choosing the space again.") from exc
        if result.returncode:
            raise ValueError("Couldn't reach that space. Choose it again from the list.")
        try:
            data = json.loads(result.stdout)
            if not isinstance(data, dict) or "result" not in data:
                raise ValueError
            return data["result"]
        except (ValueError, TypeError) as exc:
            raise ValueError("Herdr returned an unreadable space list") from exc

    def refresh(self):
        data = self.command("list")
        if not isinstance(data, dict) or not isinstance(data.get("workspaces"), list):
            raise ValueError("Herdr returned an unreadable space list")
        items = {}
        for workspace in data["workspaces"]:
            if not isinstance(workspace, dict):
                raise ValueError("Herdr returned an unreadable space list")
            link = space_link({"workspace_id": workspace.get("workspace_id"),
                               "label": workspace.get("label"), "session": self.context.session})
            items[link["workspace_id"]] = link
        self.items = items
        return list(items.values())

    def label(self, link):
        if not link:
            return "None"
        if link["session"] == self.context.session:
            return self.items.get(link["workspace_id"], link)["label"]
        return link["label"]

    def focus(self, link):
        link = space_link(link)
        if not link:
            raise ValueError("Attach a space first")
        if link["session"] != self.context.session:
            raise ValueError("This space belongs to another Herdr session. Attach one from this session to open it here.")
        self.refresh()
        if link["workspace_id"] not in self.items:
            raise ValueError("This space is closed. You can attach another one.")
        self.command("focus", link["workspace_id"])
