# Records docs/demo.gif: make demo (needs vhs and docker).
Output docs/demo.gif

Set Shell "bash"
Set FontSize 16
Set Width 1280
Set Height 760
Set Padding 12
Set Theme "Catppuccin Mocha"
Set TypingSpeed 60ms

Hide
Type "export SQLVIM_PASSWORD_SHOP=demo XDG_STATE_HOME=$(mktemp -d) PATH=$PWD/bin:$PATH && clear && sqlvim --config docs/demo"
Enter
Sleep 2s
Show

# 1. browse: shop -> shop -> public -> orders
Type "l"
Sleep 800ms
Type "jjl"
Sleep 800ms
Type "jl"
Sleep 800ms
Type "jjl"
Sleep 1.5s

# 2. filter by the status under the cursor, sort by total, clear the filter
Type "ll"
Sleep 500ms
Type "ff"
Sleep 1.5s
Type "l"
Type "s"
Sleep 1.5s
Type "fx"
Sleep 1s

# 3. search everything
Space
Space
Sleep 500ms
Type@120ms "cust"
Sleep 1s
Enter
Sleep 1.5s

# 4. staged edit and SQL preview
Type "lll"
Sleep 300ms
Type "i"
Sleep 600ms
Escape
Type "cc"
Type "Porto"
Sleep 300ms
Enter
Sleep 800ms
Space
Type "w"
Sleep 2.5s
Type "n"
Sleep 300ms
Type "U"
Sleep 800ms

# 5. console with completion
Space
Type "c"
Sleep 1s
Type "i"
Type@70ms "SELECT c.city, count(*) AS orders, sum(o.total) FROM orders o JOIN customers c ON c.id = o.customer_id GROUP BY 1 ORDER BY 3 DESC;"
Escape
Sleep 500ms
Enter
Sleep 2s
Ctrl+J
Sleep 2.5s

Type ":qa"
Enter
Sleep 500ms
