enum Shape {
  case Circle, Empty
  case Square(side: Int)
  def render(): Unit = helper()
  def helper(): Unit = ()
}
