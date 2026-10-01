Imports System
Namespace App
    Module Helpers
        Public Sub Log(Optional text As String = "ok")
        End Sub
    End Module
    Partial Class Worker
        Public Event Changed()
        Private count As Integer
        Public Property Name As String
        Public Sub New()
            Helpers.Log()
        End Sub
        Public Sub Run()
            Helpers.Log("hello")
            App.Helpers.Log()
            Me.Local()
            Later()
            Dim value As Object
            value.Log()
        End Sub
        Private Sub Local() Handles Me.Changed
            RaiseEvent Changed()
        End Sub
    End Class
    Enum State
        Ready
        Done
    End Enum
End Namespace
