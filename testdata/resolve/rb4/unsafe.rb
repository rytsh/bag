class Dyn < Billing::Base
  attr_accessor :x
  def call_it
    compute
  end
end
Billing::Base.class_eval { }
