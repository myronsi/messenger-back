import ast
import io
import logging
import re
import unittest
from pathlib import Path

from server.logging_config import RedactSecretsFilter

SERVER_DIR = Path(__file__).resolve().parents[1] / "server"
LOG_METHODS = {"debug", "info", "warning", "warn", "error", "exception", "critical", "log"}
FORBIDDEN = re.compile(
    r"share|master_?key|part[12]|password|token|secret|\bcontent\b|\bdata\b|\bmessage\b|\breaction\b|"
    r"file_name|file_url|file_path|\busername\b|stdout|stderr|display_name|\bbody\b|payload|\bshares\b",
    re.IGNORECASE,
)
SAFE_EXPRESSIONS = {"message.get('type')", 'message.get("type")'}


def _log_calls(tree):
    for node in ast.walk(tree):
        if (
            isinstance(node, ast.Call)
            and isinstance(node.func, ast.Attribute)
            and node.func.attr in LOG_METHODS
            and isinstance(node.func.value, ast.Name)
            and node.func.value.id in {"logger", "logging"}
        ):
            yield node


def _logged_expressions(source, call):
    """Expressions whose values end up in the log line: f-string fields and %-style arguments."""
    for arg in call.args:
        for sub in ast.walk(arg):
            if isinstance(sub, ast.FormattedValue):
                yield ast.get_source_segment(source, sub.value) or ""
    for arg in call.args[1:]:
        if not isinstance(arg, ast.JoinedStr):
            yield ast.get_source_segment(source, arg) or ""


class LogHygieneTests(unittest.TestCase):
    def test_no_print_or_basicconfig_in_server(self):
        offenders = []
        for path in SERVER_DIR.rglob("*.py"):
            if path.name == "logging_config.py":
                continue
            tree = ast.parse(path.read_text(encoding="utf-8"))
            for node in ast.walk(tree):
                if not isinstance(node, ast.Call):
                    continue
                func = node.func
                if isinstance(func, ast.Name) and func.id == "print":
                    offenders.append(f"{path.name}:{node.lineno} print()")
                if (
                    isinstance(func, ast.Attribute)
                    and func.attr == "basicConfig"
                    and isinstance(func.value, ast.Name)
                    and func.value.id == "logging"
                ):
                    offenders.append(f"{path.name}:{node.lineno} logging.basicConfig()")
        self.assertEqual(offenders, [])

    def test_log_statements_do_not_interpolate_secrets_or_content(self):
        offenders = []
        for path in SERVER_DIR.rglob("*.py"):
            source = path.read_text(encoding="utf-8")
            for call in _log_calls(ast.parse(source)):
                for expr in _logged_expressions(source, call):
                    if expr in SAFE_EXPRESSIONS:
                        continue
                    if FORBIDDEN.search(re.sub(r"[\w.]*_(?:id|type)\b", "", expr)):
                        offenders.append(f"{path.name}:{call.lineno} logs {expr!r}")
        self.assertEqual(offenders, [])

class RedactSecretsFilterTests(unittest.TestCase):
    def _emit(self, msg, *args):
        stream = io.StringIO()
        handler = logging.StreamHandler(stream)
        handler.addFilter(RedactSecretsFilter())
        logger = logging.getLogger("test.redact")
        logger.propagate = False
        logger.handlers = [handler]
        logger.setLevel(logging.INFO)
        logger.info(msg, *args)
        return stream.getvalue()

    def test_websocket_token_in_url_is_masked(self):
        out = self._emit('%s - "WebSocket %s" [accepted]', "1.2.3.4:5", "/ws/chat/0?token=abc.def.ghi")
        self.assertNotIn("abc.def.ghi", out)
        self.assertIn("/ws/chat/0?token=[REDACTED]", out)

    def test_other_query_params_are_kept(self):
        out = self._emit("GET %s", "/messages/history?chat_id=3&token=secret&limit=20")
        self.assertNotIn("secret", out)
        self.assertIn("chat_id=3", out)
        self.assertIn("limit=20", out)


if __name__ == "__main__":
    unittest.main()
