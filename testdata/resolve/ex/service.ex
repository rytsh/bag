defmodule App.Service do
  alias App.Repo
  import App.Repo
  def run, do: Repo.save(1)
end
