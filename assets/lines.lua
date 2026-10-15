-- Each source line becomes its own paragraph. The line-centric format puts
-- one thought or rule per line; a blank line still separates blocks.
function Para(el)
  local paras, cur = {}, {}
  for _, inline in ipairs(el.content) do
    if inline.t == "SoftBreak" then
      if #cur > 0 then table.insert(paras, pandoc.Para(cur)) end
      cur = {}
    else
      table.insert(cur, inline)
    end
  end
  if #cur > 0 then table.insert(paras, pandoc.Para(cur)) end
  if #paras <= 1 then return nil end
  return pandoc.Div(paras, pandoc.Attr("", { "lines" }))
end
