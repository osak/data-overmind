# AI Doc Template レビューフィードバック機能 実装計画

- 最終更新日時: 2026-09-24 23:25 JST
- ステータス: Implemented
- 主対象読者: 実装を担当する AI エージェント
- 対象リポジトリ: `data-overmind`, `ai-doc-template`

## 1. 目的

`ai-doc-template` で生成した HTML を `data-overmind fileserver` 経由で閲覧しているとき、人間が段落単位でレビューコメントを付け、元 HTML と同じディレクトリに機械可読なレビュー結果を保存できるようにする。

保存されたレビュー結果は、後続の AI エージェントが元 HTML と一緒に読み、指摘箇所を特定して修正に反映できる形式にする。

完成時の利用フローは次のとおり。

1. 人間が `data-overmind fileserver` から `ai-doc-template` の HTML を開く。
2. 各段落の右側に薄く表示される三点リーダーボタンからコメント欄を開く。
3. 入力内容は随時 `localStorage` に保存される。
4. 目次サイドバー内の「レビューをサブミットする」を押す。
5. `data-overmind` が元 HTML の隣に `<元ファイル名>.review.json` を原子的に保存する。
6. AI は HTML と JSON を読み、引用文と段落アンカーを使って指摘を適用する。

## 2. スコープ

### 2.1 今回実装するもの

- `.doc-body` 内の `p` と `li` 要素をコメント対象にする。
- 段落ごとのコメント入力 UI を `ai-doc-template` の既存 CSS / JavaScript に追加する。
- 入力中コメントの `localStorage` へのデバウンス保存と、ページ離脱直前の同期保存を行う。
- `data-overmind` の存在を検出する同一オリジンのコンテキスト API を追加する。
- レビュー一式を保存する API を追加する。
- 元 HTML と同じディレクトリに、決定的な名前の JSON ファイルを保存する。
- 元 HTML の変更とレビューの同時更新を検出し、上書き事故を防止する。
- キーボード操作、フォーカス表示、狭い画面、ダークテーマ、印刷に対応する。
- Go 側の自動テストと、ブラウザでの手動確認用フィクスチャを用意する。

### 2.2 今回は実装しないもの

- ユーザー認証、レビュアーの個人識別、複数レビュアーの統合。
- コメントへの返信、スレッド、リアクション、承認ワークフロー。
- コメント履歴の無制限保存。MVP では HTML ごとに 1 個の現在レビューを保持する。
- AI の自動起動や自動修正。今回は AI が確実に読める入力ファイルを作るところまでとする。
- 見出し、表、図、コードブロックへのコメント。
- `file://` で直接開いた HTML からのサブミット。
- 任意パスへのファイル保存や、一般用途の書き込み API。

## 3. 現状と制約

### 3.1 data-overmind

- `internal/fileserver/server.go` の `fileServer` が、起動時カレントディレクトリをルートとする読み取り専用ブラウザを提供している。
- HTML は変換せず raw file として別タブで開く。
- `serveHTTP` は現在 `GET` と `HEAD` 以外を拒否する。
- `resolve` は symlink を解決した実パスがルート外へ出ないことを検証している。
- 書き込み機能はまだなく、セキュリティ境界を崩さないことが重要である。

### 3.2 ai-doc-template

- `assets/doc.js` は目次、見出しアンカー、コードコピー、表ラップ等を実行時に追加する。
- `assets/doc.css` はライト / ダーク、レスポンシブ、A4 印刷を担当する。
- `window.DOC_CONFIG` を深いマージで上書きできる。
- JavaScript が動かなくても本文が読めること、分離配置と単一ファイル配置の両方を保つことが不変条件である。
- 文書の作者にレビュー用クラスや ID の手書きを要求してはならない。

## 4. 採用する設計

### 4.1 境界

2 リポジトリを HTTP 上の小さなプロトコルで分離する。

- `ai-doc-template` は、同一オリジンの well-known API を呼び出すブラウザクライアントである。
- `data-overmind` は、元文書の同定、競合検出、入力検証、JSON 保存を担当する。
- `ai-doc-template` は `data-overmind` の Go 実装やディレクトリ構造に依存しない。
- `data-overmind` は HTML を書き換えたり、レビュー UI を注入したりしない。

通常の Web サーバー、静的ホスティング、または `file://` で HTML を開いた場合、API 検出に失敗するためレビュー UI は表示されない。本文閲覧と既存機能はそのまま動く。

### 4.2 保存ファイル

元ファイルが `docs/architecture.html` の場合、保存先は次に固定する。

```text
docs/architecture.html
docs/architecture.html.review.json
```

`.html` を残したまま `.review.json` を付ける。`architecture.review.json` ではなく `architecture.html.review.json` とすることで、同じ basename の `.htm` や他形式との衝突を避ける。

相互参照は次の 3 点で成立させる。

1. レビューファイル名が元ファイル名から決定的に導出できる。
2. JSON の `source.path` が配信ルートからの元 HTML 相対パスを保持する。
3. コンテキスト API が `sourceURL` と `reviewURL` の両方を返す。

元 HTML 自体は変更しない。レビュー提出のたびに本文へリンクを追記すると、未コミット差分、ハッシュ不一致、並行編集を発生させるためである。

### 4.3 レビュー対象とアンカー

対象は `.doc-body` 配下の `p` と `li` とする。各対象に、実行時に次の情報を割り当てる。

- `block.id`: `data-review-id` があればその値。なければ `sectionId::p-N` または `sectionId::li-N`。
- `block.sectionId`: 直前の `h2` または `h3` の ID。見出し前は `preamble`。
- `block.paragraphIndex`: schema v1 との互換名。レビュー対象全体における 0 始まりの位置。
- `block.sectionParagraphIndex`: schema v1 との互換名。同じ section 内の同種要素における 0 始まりの位置。
- `block.quote`: 段落の `textContent` を空白正規化し、32 KiB の API 上限内に収めた引用。
- `block.textSHA256`: 保存時にサーバーが正規化済み `quote` から計算する SHA-256。

通常は作者が `data-review-id` を書く必要はない。長期間安定させたい特殊な文書だけ、任意属性として明示できるようにする。子に `p` を持つ `li` は、操作の二重表示を避けるため子の `p` だけを対象にする。

AI がコメント対象を解決するときの優先順は以下とする。

1. `data-review-id` または生成された `block.id` が一致する段落。
2. `block.textSHA256` が一致する段落。
3. `block.quote` の完全一致。
4. `sectionId` と段落インデックスを使った候補提示。

AI がレビューを読む時点では元 HTML は通常未変更なので、複数の手掛かりを持たせることで十分な特定精度を得られる。

## 5. HTTP API 契約

API prefix は実ファイルとの衝突を避けるため `/_data-overmind/` に固定する。

### 5.1 コンテキスト取得

```http
GET /_data-overmind/review-context?document=%2Fdocs%2Farchitecture.html
Accept: application/json
```

成功時は `200 application/json` を返す。

```json
{
  "schemaVersion": 1,
  "source": {
    "path": "docs/architecture.html",
    "url": "/docs/architecture.html",
    "sha256": "sha256:...",
    "size": 42891,
    "modifiedAt": "2026-09-24T21:30:00+09:00"
  },
  "review": {
    "path": "docs/architecture.html.review.json",
    "url": "/docs/architecture.html.review.json",
    "revision": null,
    "document": null
  }
}
```

既存レビューがある場合、`review.revision` はレビューファイル内容の `sha256:` 値、`review.document` は検証済みレビュー JSON とする。存在しない場合は両方 `null` とする。

エラーは以下を使う。

- `400`: query 不正、URL path でない、サイズ上限等。
- `404`: 元ファイルが存在しない、通常ファイルでない、HTML でない、ルート外へ出る。
- `500`: 読み取りに失敗した。

### 5.2 レビュー保存

決定的なリソースを置き換えるため `POST` ではなく `PUT` を使う。

```http
PUT /_data-overmind/reviews?document=%2Fdocs%2Farchitecture.html
Content-Type: application/json
X-Data-Overmind-Review: 1
```

リクエスト例:

```json
{
  "schemaVersion": 1,
  "sourceSHA256": "sha256:...",
  "baseRevision": null,
  "documentTitle": "アーキテクチャ設計書",
  "comments": [
    {
      "id": "0199...",
      "block": {
        "id": "背景::p-0",
        "sectionId": "背景",
        "paragraphIndex": 3,
        "sectionParagraphIndex": 0,
        "quote": "現状の処理は…",
        "textSHA256": "sha256:..."
      },
      "comment": "この判断の根拠となる計測値を追記してください。",
      "createdAt": "2026-09-24T21:31:10.123+09:00",
      "updatedAt": "2026-09-24T21:33:02.455+09:00"
    }
  ]
}
```

サーバーは `source.path`, `review.path`, `submittedAt`, `source.sha256`, `review.revision` を信頼境界の内側で確定し、保存文書を組み立てる。成功時は `200`、初回作成時は `201` とし、保存したレビュー文書と新しい revision を返す。

保存 JSON のトップレベル構造:

```json
{
  "schemaVersion": 1,
  "source": {
    "path": "docs/architecture.html",
    "sha256": "sha256:...",
    "title": "アーキテクチャ設計書"
  },
  "review": {
    "path": "docs/architecture.html.review.json",
    "revision": "sha256:...",
    "submittedAt": "2026-09-24T21:33:10.000+09:00"
  },
  "comments": []
}
```

`revision` は、revision フィールド自身を除く正規化済み保存内容から計算する。実装を単純化する場合は、保存後の全ファイル SHA-256 を API レスポンスだけに返し、ファイル内部の `review.revision` を省略してもよい。ただしコンテキスト API と競合判定で同じ定義を使うこと。

### 5.3 競合検出

- 現在の元 HTML SHA-256 が `sourceSHA256` と異なる場合は `409 source_changed`。
- 現在のレビュー revision が `baseRevision` と異なる場合は `409 review_changed`。
- 競合時は既存ファイルを一切変更しない。
- 応答には現在の source hash / review revision を含め、UI はコメントを `localStorage` に保持したまま再読み込みを促す。

### 5.4 入力制限

- body 上限: 1 MiB。
- comments 上限: 500 件。
- `comment`: 空白だけを拒否し、1 件 16 KiB 以下。
- `quote`: 1 件 32 KiB 以下。
- ID / section ID: 256 byte 以下。
- `documentTitle`: 1 KiB 以下。
- 未知フィールドは `json.Decoder.DisallowUnknownFields` で拒否する。
- JSON の後ろに 2 個目の値があれば拒否する。
- 対象 source は `.html` または `.htm` の通常ファイルに限定する。

### 5.5 書き込み安全性

- source path は既存 `resolve` と同じ境界検証を通す。
- 保存先は source の実ディレクトリと basename からサーバーが導出し、クライアントに指定させない。
- 保存先親ディレクトリもルート配下の実パスであることを再確認する。
- 一時ファイルを同じディレクトリに作成し、`Sync`、`Close`、`Rename` の順で原子的に置換する。
- パーミッションは `0644` を既定にし、既存ファイル更新時は既存 mode を維持する。
- 保存 JSON は 2 space indent、末尾 newline 付きとし、Git diff と AI 読み取りを安定させる。

### 5.6 ブラウザからの不正書き込み対策

- CORS ヘッダーは付与しない。
- `Origin` がある場合、現在の request scheme + host と完全一致しなければ `403`。
- `Content-Type: application/json` を必須にする。
- `X-Data-Overmind-Review: 1` を必須にする。通常の HTML form からは送れない。
- API への `OPTIONS` は許可しないため、クロスオリジン preflight は成功しない。
- 書き込み先を sibling review file に限定し、一般的なファイル上書き能力を与えない。

## 6. ブラウザ UI と状態管理

### 6.1 機能検出

`DOC_CONFIG` に次を追加する。

```js
review: {
  enable: 'auto',
  contextURL: '/_data-overmind/review-context',
  submitURL: '/_data-overmind/reviews',
  selector: 'p, li',
  storagePrefix: 'ai-doc-review:v1:',
  saveDelay: 300
}
```

- `false`: API を呼ばず、レビュー UI を無効化。
- `'auto'`: HTTP(S) のときだけ context API を呼び、成功時だけ UI を表示。404 やネットワーク失敗は静かに無効化。
- `true`: API を呼ぶ。失敗時は目次メニューに「レビュー機能に接続できません」と表示し、本文はそのまま利用可能にする。

初期化順は、見出し ID 付与、目次作成、レビューコンテキスト取得、レビュー UI 生成とする。レビュー UI が後から入る可能性があるため、見出しが少ないときも `.doc-toc` 自体を DOM から削除せず空で残す。CSS の `:empty` で従来どおり幅は取らない。

### 6.2 段落操作

- 各段落に JavaScript で `position` 用の内部クラスを付け、右ガターに `…` ボタンを置く。
- 未コメントのボタンは通常は薄く、段落 hover、段落内 focus、ボタン focus で明瞭にする。
- コメントがある段落は常時アクセント色で表示し、スクリーンリーダー向けラベルに状態を含める。
- ボタンで 1 個のコメントパネルを開く。同時に開くパネルは 1 個まで。
- パネルには対象段落の短い引用、`textarea`、閉じる操作、コメント削除操作を置く。
- `Escape` で閉じ、閉じた後は元の三点ボタンへフォーカスを戻す。
- 狭い画面では右ガターを使わず、パネルを段落直下にインライン表示する。
- UI の文言は日本語を既定とし、設定値で将来差し替えられる構造にする。

### 6.3 目次メニュー

`#doc-toc` 内の目次の下に次を追加する。

- コメント件数。
- 保存状態: `保存済み`, `入力を保存中`, `ローカル保存に失敗`。
- 「レビューをサブミットする」ボタン。
- 送信中 / 成功 / 競合 / 通信失敗のステータス。

空コメントは件数と payload に含めない。非空コメントが 0 件で既存レビューもない場合、submit ボタンは disabled にする。既存レビューがある場合は、全コメントを削除した状態も保存できる。送信成功時は保存先 review path を表示する。

### 6.4 localStorage

キーは source hash ごとに分離する。

```text
ai-doc-review:v1:<URL-encoded pathname>:<source sha256>
```

保存値は `schemaVersion`, `sourceSHA256`, `baseRevision`, `comments`, `updatedAt` を含む。

- `input` ごとにメモリを更新し、300 ms debounce で保存する。
- `blur`, `visibilitychange` の hidden 遷移、`pagehide` では同期的に保存する。
- quota / privacy mode 等で失敗した場合は、入力を消さずにメニューへ警告を出す。
- submit に成功したら、サーバー応答を基準状態として保存した後、その draft key を削除する。
- submit に失敗した場合は draft を残す。
- source hash が変わると別 key になるため、古い文書向け draft を誤適用しない。古い draft は自動削除しない。
- 同じ source hash かつ同じ base revision の draft は完全なローカル状態として復元し、コメント削除も維持する。base revision が異なる場合だけ comment ID 単位で `updatedAt` が新しい方を採用し、片方だけにある comment も保持する。

## 7. ai-doc-template の変更計画

### 7.1 `assets/doc.js`

1. `DEFAULTS.review` を追加する。
2. SHA-256、UUID、空白正規化、日時生成、localStorage の小さな helper を追加する。
3. 段落アンカー生成を純粋関数に近い形で分離する。
4. context API client とエラー分類を追加する。
5. draft load / merge / debounce save を追加する。
6. 段落ボタン、コメントパネル、目次内 submit panel を生成する。
7. PUT submit と 409 表示を追加する。
8. `init()` から非同期レビュー初期化を開始する。失敗は既存機能を止めない。
9. `window.DocTemplate` にレビューの内部状態は公開せず、必要なら `reviewStatus()` の読み取り専用 API だけを公開する。

レビュー UI の DOM は JavaScript が生成し、文書作者が書く HTML の語彙を増やさない。

### 7.2 `assets/doc.css`

1. 既存 `:root` に review 用の色・ガター幅・パネル幅トークンを追加する。
2. 段落ボタン、入力パネル、目次アクション、状態表示を追加する。
3. light / dark の両テーマで既存 semantic color token を再利用する。
4. `focus-visible`, hover, commented state を色以外でも識別できるようにする。
5. `@media (max-width: ...)` でインライン配置へ切り替える。
6. `@media print` では全レビュー UI を `display: none` にする。
7. `prefers-reduced-motion` の既存規則に従う。

レビュー用クラスは JavaScript 内部実装であり、AI が本文生成時に覚える許可クラスには追加しない。

### 7.3 `template.html`, `SKILL.md`, `references/config.md`, `README.md`

- `DOC_CONFIG.review` の設定項目を追記する。
- `data-overmind` 経由でのみ自動有効になることを説明する。
- sibling review file の命名規則と AI の読み方を説明する。
- JavaScript 無効、直接ファイル表示、単一ファイル化で本文が壊れないことを明記する。

### 7.4 手動確認用 fixture

`examples/review.html` または既存 `example.html` に、短文、長文、リスト項目、callout 内段落、重複文、見出し前段落を含むレビュー対象を置く。通常の配布用 example にレビュー専用内容を混ぜたくない場合は専用 fixture を選ぶ。

## 8. data-overmind の変更計画

### 8.1 ファイル構成

候補構成:

```text
internal/fileserver/
  server.go              # routing と既存 file serving
  review.go              # API handler、validation、atomic write
  review_test.go         # API / path / conflict / write tests
```

既存 `resolve` とルート情報を再利用する。レビュー機能を別 package に分けるほどの公開再利用性はまだない。

### 8.2 routing

`serveHTTP` 冒頭で `/_data-overmind/` を通常ファイルより先に判定する。

- review context は `GET` のみ。
- review save は `PUT` のみ。
- その他は `404`。
- 通常ファイル系 route は従来どおり `GET` / `HEAD` のみ。

これにより `/_data-overmind/...` という実ファイルがルート内にあっても API が優先される。予約 prefix として README に明記する。

### 8.3 永続化

- 保存前に request source hash と現在の HTML hash を比較する。
- 既存 review file があれば parse / schema validation し、revision を計算する。
- request の base revision と比較する。
- サーバー管理フィールドを埋めた canonical document を `json.MarshalIndent` する。
- sibling temp file に書き、原子的 rename する。
- 成功後の revision と document を返す。

壊れた既存 review JSON がある場合は上書きせず `409 invalid_existing_review` とする。手動復旧できる内容を温存するためである。

### 8.4 README

- fileserver が HTML review companion だけを書き込めるようになったことを説明する。
- `0.0.0.0` bind 時は LAN 内クライアントもレビューを保存できることを警告する。
- review file の命名と、機能を使わない場合は従来どおり読み取り専用であることを説明する。

## 9. 実装結果

計画した API、段落 UI、下書き保存、競合検出、原子的保存、ドキュメント更新を実装した。
Go 自動テスト、standalone build、実ブラウザでの下書き復元・送信・再読込、ライト / ダーク、
印刷プレビューを確認済みである。手動確認用 fixture はリポジトリ直下の
`review-example.html` とした。

## 10. テスト計画

### 10.1 Go 自動テスト

- HTML の context が source hash と sibling review path を返す。
- URL encoded 日本語 / 空白を含む path を扱える。
- 非 HTML、directory、不在、ルート外 symlink を拒否する。
- 初回 PUT が `201` で review file を作る。
- 2 回目の同一 revision PUT が `200` で置換する。
- source hash 不一致を `409` にする。
- base revision 不一致を `409` にする。
- 壊れた既存 review file を温存して `409` にする。
- body / comments / comment text の上限を拒否する。
- unknown JSON field、trailing JSON、空コメントを拒否する。
- Content-Type、custom header、method、Origin を検証する。
- symlink を使った root 外書き込みができない。
- 成功時 JSON が indent + newline で安定する。
- 書き込み失敗時に既存 review file が変化しない。
- 従来の browsing / raw file / download テストがすべて残る。

### 10.2 ブラウザ確認

- `data-overmind` 経由では review UI が出る。
- `file://` と通常静的サーバーでは UI が出ず、コンソールエラーを乱発しない。
- 入力後 300 ms 程度で localStorage に保存される。
- reload で draft が復元される。
- submit 成功で sibling JSON が作成され、再読込でもコメントが見える。
- API を一時的に失敗させても draft が残る。
- source / review conflict 時に上書きしない。
- 三点ボタンと textarea をキーボードだけで操作できる。
- Escape と focus return が機能する。
- 長い段落、callout 内段落、連続段落で UI が重ならない。
- 375 px 幅で横スクロールを発生させない。
- light / dark の可読性を確認する。
- 印刷プレビューに review UI が出ない。
- standalone HTML を `data-overmind` で開いても動く。

## 11. 受け入れ条件

- 人間は各 `.doc-body p` と `.doc-body li` に対し 3 操作以内でコメント入力を開始できる。
- reload や一時的な submit 失敗で未送信コメントが失われない。
- submit により元 HTML と同じディレクトリへ `.review.json` が 1 個作成される。
- クライアントは保存先を任意指定できない。
- 元 HTML が変わった状態で古いレビューを上書きできない。
- AI が JSON の `source.path`, `block.quote`, `comment` だけでも修正対象と要求を理解できる。
- review API がない環境で、既存の表示・目次・コピー・テーマ・印刷が回帰しない。
- JavaScript が無効でも本文は完全に読める。
- `go test ./...` と `node scripts/build-standalone.mjs example.html` が成功する。

## 12. AI がレビューを適用するときの手順

1. `<document>.review.json` の `source.path` から元 HTML を開く。
2. 現在の HTML SHA-256 と `source.sha256` を比較する。異なる場合は、古いレビューであることをユーザーへ明示してから対応する。
3. `comments` を上から読み、`block.id`、`textSHA256`、`quote` の順で対象段落を特定する。
4. コメントの意図を満たすよう元 HTML を修正する。コメント文をそのまま本文へ貼らない。
5. `ai-doc-template` の構造制約、セマンティック HTML、見出し階層を維持する。
6. standalone 版がある場合はソース版から再生成する。
7. ブラウザで表示確認し、対象コメントごとの対応結果を報告する。
8. review JSON の削除や status 変更は、ユーザーが明示的に依頼した場合だけ行う。

## 13. リスクと緩和策

### ローカルサーバーが書き込み可能になる

レビュー専用の決定的 sibling path だけを許し、custom header、same-origin、JSON content type、サイズ上限を必須にする。一般ファイル API は作らない。

### DOM 位置が文書編集でずれる

位置だけでなく section ID、引用全文、引用 hash を保存する。明示 `data-review-id` は任意の安定化手段として残す。

### localStorage 容量不足

1 comment 16 KiB、全体 1 MiB の上限に合わせ、失敗を UI に表示する。メモリ上の入力は消さない。

### TOC のない短い文書で submit UI が消える

`.doc-toc` を削除せず、レビュー UI が入れば sidebar として表示されるように初期化を変更する。

### 既存の「読み取り専用」という期待が変わる

README と起動ログに review companion のみ書き込み可能であることを明記する。将来必要なら `--reviews=false` フラグを追加できるが、MVP では HTML 側からの API 未使用時に書き込みは起きない。

## 14. 決定事項

- 保存形式は Markdown ではなく JSON とする。UI の再ロード、revision、機械的 validation を優先し、AI 可読性は明示的なフィールド名と整形出力で確保する。
- 保存は履歴追加ではなく、HTML ごとの単一 companion file の置換とする。
- 対象要素は `p` と `li` とする。
- 元 HTML はレビュー提出時に変更しない。
- レビュー機能は `DOC_CONFIG.review.enable = 'auto'` を既定とし、data-overmind の context API 成功時だけ現れる。
- API の保存 method は冪等性を表す `PUT` とする。
- 競合時は自動マージせず、local draft を保持して再読込を促す。

## 15. 将来拡張

- `table`, `figure`, `pre` へのコメント対象拡張。
- 複数レビュアーと append-only history。
- AI 修正後の `resolved` / `rejected` 状態と対応メモ。
- レビュー一覧画面と未対応件数表示。
- source map 的な明示 `data-review-id` の自動生成支援。
- `data-overmind` から AI タスクを直接起動する操作。

これらは schema version を上げるか、後方互換フィールドを追加して実現する。初版の companion naming と source / block identity は維持する。
