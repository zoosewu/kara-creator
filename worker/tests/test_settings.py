"""在終端機執行時：沒給的欄位用問的，留白沿用上次記住的值。"""
import platform

from kara_worker import settings


def answers(*replies):
    it = iter(replies)
    prompts = []

    def read(prompt):
        prompts.append(prompt)
        return next(it)
    return read, prompts


def test_ask_only_missing_fields():
    read, prompts = answers("http://nas:8765", "pc")
    secret, secret_prompts = answers("")
    out = settings.ask({"nas": None, "name": None, "token": None}, {}, read, secret)
    assert out == {"nas": "http://nas:8765", "name": "pc", "token": ""}
    assert "（例如 http://mac-mini.local:8765）" in prompts[0]
    assert f"（{platform.node()}）" in prompts[1]
    assert "（留白 = 不用）" in secret_prompts[0]

    read, prompts = answers()
    secret, _ = answers()
    out = settings.ask({"nas": "http://a", "name": "b", "token": "c"}, {"nas": "x"}, read, secret)
    assert out == {"nas": "http://a", "name": "b", "token": "c"} and prompts == []


def test_blank_keeps_saved_values():
    saved = {"nas": "http://nas:8765", "name": "pc-4070", "token": "secret42"}
    read, prompts = answers("", "  ")
    secret, secret_prompts = answers("")
    assert settings.ask({"nas": None, "name": None, "token": None}, saved, read, secret) == saved
    assert "（http://nas:8765）" in prompts[0] and "（pc-4070）" in prompts[1]
    assert "****42" in secret_prompts[0] and "secret" not in secret_prompts[0]

    read, _ = answers("http://other", "pc-2")
    secret, _ = answers(settings.CLEAR)
    out = settings.ask({"nas": None, "name": None, "token": None}, saved, read, secret)
    assert out == {"nas": "http://other", "name": "pc-2", "token": ""}


def test_nas_required():
    read, prompts = answers("", "http://nas", "")
    secret, _ = answers("")
    assert settings.ask({"nas": None, "name": None, "token": None}, {}, read, secret)["nas"] == "http://nas"
    assert len(prompts) == 3


def test_save_and_load(tmp_path):
    path = tmp_path / "sub" / "worker.json"
    assert settings.load(path) == {}
    settings.save(path, {"nas": "http://nas", "name": "中文名", "token": ""})
    assert settings.load(path) == {"nas": "http://nas", "name": "中文名", "token": ""}
    path.write_text("not json", encoding="utf-8")
    assert settings.load(path) == {}
    path.write_text('{"nas": 1, "name": "x", "other": "y"}', encoding="utf-8")
    assert settings.load(path) == {"name": "x"}


def test_settings_path(monkeypatch, tmp_path):
    monkeypatch.setenv("KARA_WORKER_SETTINGS", str(tmp_path / "w.json"))
    assert settings.settings_path() == tmp_path / "w.json"
