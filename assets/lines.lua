-- Each source line becomes its own paragraph. The line-centric format puts
-- one thought or rule per line. A blank line separates paragraphs too, and
-- every gap is the same.
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
  return paras
end
